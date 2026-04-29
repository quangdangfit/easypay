package blockchain

import (
	"context"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/pkg/logger"
)

// Reconciler is Layer 3: every Interval, scan all still-pending pending_txs
// for this chain and re-fetch the receipt over HTTP RPC. Any tx that has
// gone missing (reorged) or whose receipt status is failure gets corrected.
//
// It also raises an alert log line for any pending_tx older than StuckAfter.
type Reconciler struct {
	Client     ChainClient
	Cfg        ChainConfig
	PendingTxs repository.PendingTxRepository
	Interval   time.Duration
	StuckAfter time.Duration
	BatchSize  int
}

func NewReconciler(c ChainClient, cfg ChainConfig, repo repository.PendingTxRepository) *Reconciler {
	return &Reconciler{
		Client: c, Cfg: cfg, PendingTxs: repo,
		Interval:   time.Hour,
		StuckAfter: 30 * time.Minute,
		BatchSize:  500,
	}
}

func (r *Reconciler) Run(ctx context.Context) error {
	log := logger.L().With("component", "blockchain.reconciler", "chain_id", r.Cfg.ChainID)
	tk := time.NewTicker(r.Interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tk.C:
			if err := r.tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Warn("reconcile tick failed", "err", err)
			}
		}
	}
}

func (r *Reconciler) tick(ctx context.Context) error {
	pendings, err := r.PendingTxs.ListPendingByChain(ctx, r.Cfg.ChainID, r.BatchSize)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, p := range pendings {
		receipt, err := r.Client.TransactionReceipt(ctx, common.HexToHash(p.TxHash))
		switch {
		case err != nil || receipt == nil:
			_ = r.PendingTxs.UpdateConfirmations(ctx, p.TxHash, p.Confirmations, domain.PendingTxStatusReorged)
		case receipt.Status != 1:
			_ = r.PendingTxs.UpdateConfirmations(ctx, p.TxHash, p.Confirmations, domain.PendingTxStatusFailed)
		}
		if now.Sub(p.CreatedAt) > r.StuckAfter {
			logger.L().Warn("on-chain pending tx stuck > threshold (Layer 4 alert)",
				"tx_hash", p.TxHash, "order_id", p.OrderID, "age", now.Sub(p.CreatedAt))
		}
	}
	return nil
}
