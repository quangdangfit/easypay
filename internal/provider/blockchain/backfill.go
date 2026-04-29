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

// BackfillScanner periodically queries eth_getLogs from the saved cursor up to
// chain head over HTTP RPC, persisting any events the WebSocket subscriber
// missed (Layer 2 of the 4-layer defense).
type BackfillScanner struct {
	Client     ChainClient
	Cfg        ChainConfig
	Cursor     CursorStore
	PendingTxs repository.PendingTxRepository
	Interval   time.Duration
	BatchSize  uint64
}

func NewBackfillScanner(c ChainClient, cfg ChainConfig, cur CursorStore, repo repository.PendingTxRepository) *BackfillScanner {
	return &BackfillScanner{
		Client: c, Cfg: cfg, Cursor: cur, PendingTxs: repo,
		Interval:  5 * time.Minute,
		BatchSize: 5000,
	}
}

func (b *BackfillScanner) Run(ctx context.Context) error {
	log := logger.L().With("component", "blockchain.backfill", "chain_id", b.Cfg.ChainID)
	tk := time.NewTicker(b.Interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tk.C:
			if err := b.tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Warn("backfill tick failed", "err", err)
			}
		}
	}
}

func (b *BackfillScanner) tick(ctx context.Context) error {
	from, err := b.Cursor.Get(ctx, b.Cfg.ChainID)
	if err != nil {
		return fmt.Errorf("cursor get: %w", err)
	}
	if from == 0 {
		from = b.Cfg.StartBlock
	}
	head, err := b.Client.BlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("block number: %w", err)
	}
	if head <= from {
		return nil
	}

	for from < head {
		to := from + b.BatchSize
		if to > head {
			to = head
		}
		q := ethereum.FilterQuery{
			FromBlock: new(big.Int).SetUint64(from),
			ToBlock:   new(big.Int).SetUint64(to),
			Addresses: []common.Address{b.Cfg.ContractAddress},
		}
		if b.Cfg.EventTopic != (common.Hash{}) {
			q.Topics = [][]common.Hash{{b.Cfg.EventTopic}}
		}
		logs, err := b.Client.FilterLogs(ctx, q)
		if err != nil {
			return fmt.Errorf("filter logs: %w", err)
		}
		for _, lg := range logs {
			b.persist(ctx, lg)
		}
		if err := b.Cursor.Set(ctx, b.Cfg.ChainID, to); err != nil {
			logger.L().Warn("cursor set failed", "err", err)
		}
		from = to
	}
	return nil
}

func (b *BackfillScanner) persist(ctx context.Context, lg types.Log) {
	hash := lg.TxHash.Hex()
	if existing, err := b.PendingTxs.GetByTxHash(ctx, hash); err == nil && existing != nil {
		return
	}
	parsed, err := ParsePaymentEvent(lg)
	if err != nil {
		return
	}
	tx := &domain.PendingTx{
		TxHash:          hash,
		BlockNumber:     lg.BlockNumber,
		OrderID:         parsed.OrderID,
		Payer:           parsed.Payer.Hex(),
		Token:           parsed.Token.Hex(),
		Amount:          new(big.Int).Set(parsed.Amount),
		ChainID:         b.Cfg.ChainID,
		RequiredConfirm: b.Cfg.RequiredConfirmations,
		Status:          domain.PendingTxStatusPending,
	}
	if err := b.PendingTxs.Create(ctx, tx); err != nil {
		// Likely a UNIQUE conflict from a race with the subscriber — ignore.
		return
	}
}
