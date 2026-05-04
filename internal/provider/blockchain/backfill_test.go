package blockchain

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/quangdangfit/easypay/internal/domain"
)

func TestBackfill_Tick_NoNewBlocks(t *testing.T) {
	chain := &fakeChain{blockNum: 50}
	cur := newMemCursor()
	cur.v[1] = 100
	b := NewBackfillScanner(chain, ChainConfig{ChainID: 1}, cur, newPendingTxStore(t).mock, newTxStore(t).mock)
	if err := b.tick(context.Background()); err != nil {
		t.Fatalf("err: %v", err)
	}
}

func TestBackfill_Tick_FetchesAndPersists(t *testing.T) {
	chain := &fakeChain{
		blockNum: 200,
		logs:     []types.Log{sampleLog()},
	}
	cur := newMemCursor()
	cur.v[1] = 50
	repo := newPendingTxStore(t)
	orders := newTxStore(t, &domain.Transaction{MerchantID: "M1", OrderID: "ord-1"})
	b := NewBackfillScanner(chain,
		ChainConfig{ChainID: 1, ContractAddress: common.HexToAddress("0xC0NTRACT"), RequiredConfirmations: 12},
		cur, repo.mock, orders.mock)
	b.BatchSize = 1000
	if err := b.tick(context.Background()); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(repo.byHash) != 1 {
		t.Fatalf("expected 1 pending tx, got %d", len(repo.byHash))
	}
	for _, tx := range repo.byHash {
		if tx.MerchantID != "M1" {
			t.Fatalf("merchant_id not stamped on backfilled row: %q", tx.MerchantID)
		}
	}
}

func TestBackfill_Tick_BlockNumberError(t *testing.T) {
	chain := &fakeChain{blockNumErr: errors.New("rpc")}
	b := NewBackfillScanner(chain, ChainConfig{ChainID: 1}, newMemCursor(), newPendingTxStore(t).mock, newTxStore(t).mock)
	if err := b.tick(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestBackfill_Tick_FilterError(t *testing.T) {
	chain := &fakeChain{blockNum: 100, filterErr: errors.New("rpc filter")}
	cur := newMemCursor()
	cur.v[1] = 0
	b := NewBackfillScanner(chain, ChainConfig{ChainID: 1, StartBlock: 0}, cur, newPendingTxStore(t).mock, newTxStore(t).mock)
	if err := b.tick(context.Background()); err == nil {
		t.Fatal("expected filter error")
	}
}

func TestBackfill_Run_StopsOnCancel(t *testing.T) {
	chain := &fakeChain{}
	b := NewBackfillScanner(chain, ChainConfig{ChainID: 1}, newMemCursor(), newPendingTxStore(t).mock, newTxStore(t).mock)
	b.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := b.Run(ctx); err == nil {
		t.Fatal("expected ctx error")
	}
}
