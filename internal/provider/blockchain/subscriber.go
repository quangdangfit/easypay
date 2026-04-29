package blockchain

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/pkg/logger"
)

// Subscriber listens to chain logs over WebSocket and persists each new event
// as a pending_tx (status=pending). The block cursor is advanced AFTER the row
// is committed, so we get at-least-once delivery on restart.
type Subscriber struct {
	Client     ChainClient
	Cfg        ChainConfig
	Cursor     CursorStore
	PendingTxs repository.PendingTxRepository
	BackoffMin time.Duration
	BackoffMax time.Duration
}

func NewSubscriber(c ChainClient, cfg ChainConfig, cur CursorStore, repo repository.PendingTxRepository) *Subscriber {
	return &Subscriber{
		Client: c, Cfg: cfg, Cursor: cur, PendingTxs: repo,
		BackoffMin: time.Second,
		BackoffMax: 30 * time.Second,
	}
}

func (s *Subscriber) Run(ctx context.Context) error {
	log := logger.L().With("component", "blockchain.subscriber", "chain_id", s.Cfg.ChainID)
	backoff := s.BackoffMin
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.runOnce(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			log.Warn("subscriber loop ended; reconnecting", "err", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff = doubleCapped(backoff, s.BackoffMax)
			continue
		}
		backoff = s.BackoffMin
	}
}

func (s *Subscriber) runOnce(ctx context.Context) error {
	q := ethereum.FilterQuery{
		Addresses: []common.Address{s.Cfg.ContractAddress},
	}
	if s.Cfg.EventTopic != (common.Hash{}) {
		q.Topics = [][]common.Hash{{s.Cfg.EventTopic}}
	}
	ch := make(chan types.Log, 64)
	sub, err := s.Client.SubscribeFilterLogs(ctx, q, ch)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	defer sub.Unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-sub.Err():
			return fmt.Errorf("subscription: %w", err)
		case lg := <-ch:
			if err := s.handleLog(ctx, lg); err != nil {
				logger.L().Error("subscriber handle log", "tx", lg.TxHash.Hex(), "err", err)
			}
		}
	}
}

func (s *Subscriber) handleLog(ctx context.Context, lg types.Log) error {
	hash := lg.TxHash.Hex()
	if existing, err := s.PendingTxs.GetByTxHash(ctx, hash); err == nil && existing != nil {
		// Dedup: already known.
		return nil
	} else if err != nil && !errors.Is(err, repository.ErrPendingTxNotFound) {
		return fmt.Errorf("dedup lookup: %w", err)
	}
	parsed, err := ParsePaymentEvent(lg)
	if err != nil {
		return fmt.Errorf("parse event: %w", err)
	}
	tx := &domain.PendingTx{
		TxHash:          hash,
		BlockNumber:     lg.BlockNumber,
		OrderID:         parsed.OrderID,
		Payer:           parsed.Payer.Hex(),
		Token:           parsed.Token.Hex(),
		Amount:          new(big.Int).Set(parsed.Amount),
		ChainID:         s.Cfg.ChainID,
		Confirmations:   0,
		RequiredConfirm: s.Cfg.RequiredConfirmations,
		Status:          domain.PendingTxStatusPending,
	}
	if err := s.PendingTxs.Create(ctx, tx); err != nil {
		return fmt.Errorf("persist pending_tx: %w", err)
	}
	if err := s.Cursor.Set(ctx, s.Cfg.ChainID, lg.BlockNumber); err != nil {
		// Cursor failure isn't fatal — backfill scanner will catch up.
		logger.L().Warn("cursor advance failed", "err", err, "block", lg.BlockNumber)
	}
	return nil
}

func doubleCapped(d, max time.Duration) time.Duration {
	d *= 2
	if d > max {
		return max
	}
	return d
}
