package consumer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/quangdangfit/easypay/internal/config"
	"github.com/quangdangfit/easypay/internal/kafka"
	"github.com/quangdangfit/easypay/pkg/hmac"
	"github.com/quangdangfit/easypay/pkg/logger"
)

// MerchantCallback is the per-merchant lookup the settlement consumer
// performs on every confirmed event. The event no longer carries the
// callback URL — it lives on the merchant row, so a merchant rotating
// their callback takes effect immediately.
type MerchantCallback struct {
	URL    string
	Secret string
}

// MerchantLookup returns the callback URL and HMAC signing secret for a
// merchant. Empty URL → skip notification (not an error).
type MerchantLookup func(merchantID string) MerchantCallback

// SettlementConsumer consumes payment.confirmed and POSTs the event to the
// merchant's callback_url. Body is signed with the merchant's secret using
// HMAC-SHA256 (X-Signature). Failures are retried per message.
type SettlementConsumer struct {
	httpClient  *http.Client
	lookup      MerchantLookup
	maxAttempts int
}

// NewSettlementConsumer wires a SettlementConsumer. lookup is a function
// (rather than a repository) so tests can inject a fake without depending
// on the merchant repository concrete impl.
func NewSettlementConsumer(lookup MerchantLookup) *SettlementConsumer {
	return &SettlementConsumer{
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		lookup:      lookup,
		maxAttempts: 3,
	}
}

func (s *SettlementConsumer) NewBatch(cfg config.KafkaConfig) *kafka.BatchConsumer {
	c := kafka.NewBatchConsumer(cfg, cfg.TopicConfirmed, s)
	// Settlement is per-message; small batches keep latency low.
	c.BatchSize = 50
	c.BatchWait = 100 * time.Millisecond
	return c
}

func (s *SettlementConsumer) Handle(ctx context.Context, msgs []kafkago.Message) error {
	// We treat each message independently; surface a batch error so the
	// per-message fallback path fires.
	for _, m := range msgs {
		if err := s.HandleOne(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

func (s *SettlementConsumer) HandleOne(ctx context.Context, m kafkago.Message) error {
	var ev kafka.PaymentConfirmedEvent
	if err := json.Unmarshal(m.Value, &ev); err != nil {
		return fmt.Errorf("decode confirmed event: %w", err)
	}
	log := logger.With(ctx).With("order_id", ev.OrderID, "merchant_id", ev.MerchantID)
	cb := s.lookup(ev.MerchantID)
	if cb.URL == "" {
		log.Debug("no callback url, skipping merchant notification")
		return nil
	}
	if cb.Secret == "" {
		return fmt.Errorf("no secret for merchant %s", ev.MerchantID)
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal callback body: %w", err)
	}

	for attempt := 1; attempt <= s.maxAttempts; attempt++ {
		err = s.sendOnce(ctx, cb.URL, cb.Secret, body)
		if err == nil {
			log.Info("merchant callback delivered", "attempt", attempt)
			return nil
		}
		log.Warn("merchant callback failed", "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
	return fmt.Errorf("merchant callback exhausted retries: %w", err)
}

func (s *SettlementConsumer) sendOnce(ctx context.Context, url, secret string, body []byte) error {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	signed := append([]byte(ts+"."), body...)
	sig := hmac.Sign(secret, signed)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Signature", sig)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("merchant returned status %d", resp.StatusCode)
	}
	return nil
}
