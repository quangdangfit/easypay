package blockchain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/kafka"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/pkg/logger"
)

// ConfirmationTracker walks pending_txs every BlockTime, computes confirmation
// depth, validates against the order, and produces payment.confirmed when the
// required confirmations are reached.
type ConfirmationTracker struct {
	Client     ChainClient
	Cfg        ChainConfig
	PendingTxs repository.OnchainTxRepository
	Orders     repository.OrderRepository
	Publisher  kafka.EventPublisher
	BlockTime  time.Duration
	BatchLimit int
}

func NewConfirmationTracker(c ChainClient, cfg ChainConfig, ptx repository.OnchainTxRepository, orders repository.OrderRepository, p kafka.EventPublisher) *ConfirmationTracker {
	return &ConfirmationTracker{
		Client: c, Cfg: cfg, PendingTxs: ptx, Orders: orders, Publisher: p,
		BlockTime:  12 * time.Second,
		BatchLimit: 100,
	}
}

func (t *ConfirmationTracker) Run(ctx context.Context) error {
	log := logger.L().With("component", "blockchain.confirmation", "chain_id", t.Cfg.ChainID)
	tk := time.NewTicker(t.BlockTime)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tk.C:
			if err := t.tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Warn("confirmation tick failed", "err", err)
			}
		}
	}
}

func (t *ConfirmationTracker) tick(ctx context.Context) error {
	latest, err := t.Client.BlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("block number: %w", err)
	}
	pendings, err := t.PendingTxs.ListPendingByChain(ctx, t.Cfg.ChainID, t.BatchLimit)
	if err != nil {
		return fmt.Errorf("list pending: %w", err)
	}
	for _, p := range pendings {
		t.processOne(ctx, p, latest)
	}
	return nil
}

func (t *ConfirmationTracker) processOne(ctx context.Context, p *domain.OnchainTransaction, latest uint64) {
	log := logger.L().With("tx_hash", p.TxHash, "order_id", p.OrderID)

	// Reorg check: receipt missing means the tx is no longer canonical.
	receipt, err := t.Client.TransactionReceipt(ctx, common.HexToHash(p.TxHash))
	if err != nil || receipt == nil {
		if err := t.PendingTxs.UpdateConfirmations(ctx, p.TxHash, 0, domain.OnchainTxStatusReorged); err != nil {
			log.Warn("mark reorged failed", "err", err)
		}
		return
	}

	if receipt.Status != 1 {
		if err := t.PendingTxs.UpdateConfirmations(ctx, p.TxHash, 0, domain.OnchainTxStatusFailed); err != nil {
			log.Warn("mark failed failed", "err", err)
		}
		return
	}

	if latest < p.BlockNumber {
		return
	}
	confirmations := latest - p.BlockNumber
	if confirmations < p.RequiredConfirm {
		// Update count, leave status pending.
		_ = t.PendingTxs.UpdateConfirmations(ctx, p.TxHash, confirmations, domain.OnchainTxStatusPending)
		return
	}

	// Validate against the order before confirming. Blockchain events carry
	// only order_id, so we fall back to a global lookup (see GetByOrderIDAny
	// docstring re. merchant collision risk).
	order, err := t.Orders.GetByOrderIDAny(ctx, p.OrderID)
	if err != nil {
		log.Warn("order lookup failed", "err", err)
		return
	}
	if order.Status == domain.OrderStatusPaid || order.Status == domain.OrderStatusRefunded {
		// Already finalised.
		_ = t.PendingTxs.UpdateConfirmations(ctx, p.TxHash, confirmations, domain.OnchainTxStatusConfirmed)
		return
	}
	if p.Amount == nil || p.Amount.Int64() != order.Amount {
		log.Warn("amount mismatch", "onchain", p.Amount, "order", order.Amount)
		_ = t.PendingTxs.UpdateConfirmations(ctx, p.TxHash, confirmations, domain.OnchainTxStatusFailed)
		return
	}

	if err := t.PendingTxs.UpdateConfirmations(ctx, p.TxHash, confirmations, domain.OnchainTxStatusConfirmed); err != nil {
		log.Warn("mark confirmed failed", "err", err)
		return
	}
	if err := t.Orders.UpdateStatus(ctx, order.MerchantID, order.OrderID, domain.OrderStatusPaid, ""); err != nil {
		log.Warn("update order status failed", "err", err)
		return
	}
	confirmed := kafka.PaymentConfirmedEvent{
		OrderID:     order.OrderID,
		MerchantID:  order.MerchantID,
		Status:      string(domain.OrderStatusPaid),
		Amount:      order.Amount,
		Currency:    order.Currency,
		ConfirmedAt: time.Now().UTC().Unix(),
	}
	if err := t.Publisher.PublishPaymentConfirmed(ctx, confirmed); err != nil {
		log.Warn("publish confirmed failed", "err", err)
	}
}
