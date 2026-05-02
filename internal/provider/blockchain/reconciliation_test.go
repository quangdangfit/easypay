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

func TestReconciler_Tick_MarksReorgedWhenReceiptMissing(t *testing.T) {
	pendingRepo := newPendingTxStore(t)
	tx := &domain.OnchainTransaction{TxHash: "0xabc", ChainID: 1, Status: domain.OnchainTxStatusPending}
	pendingRepo.byHash[tx.TxHash] = tx
	chain := &fakeChain{receiptErr: errors.New("not found")}
	r := NewReconciler(chain, ChainConfig{ChainID: 1}, pendingRepo.mock)
	r.tick(context.Background())
	if tx.Status != domain.OnchainTxStatusReorged {
		t.Fatalf("status: %s", tx.Status)
	}
}

func TestReconciler_Tick_MarksFailedOnFailureReceipt(t *testing.T) {
	pendingRepo := newPendingTxStore(t)
	tx := &domain.OnchainTransaction{TxHash: "0xabc", ChainID: 1, Status: domain.OnchainTxStatusPending}
	pendingRepo.byHash[tx.TxHash] = tx
	chain := &fakeChain{receipts: map[common.Hash]*types.Receipt{common.HexToHash("0xabc"): {Status: 0}}}
	r := NewReconciler(chain, ChainConfig{ChainID: 1}, pendingRepo.mock)
	r.tick(context.Background())
	if tx.Status != domain.OnchainTxStatusFailed {
		t.Fatalf("status: %s", tx.Status)
	}
}

func TestReconciler_Tick_LeavesGoodOnesAlone(t *testing.T) {
	pendingRepo := newPendingTxStore(t)
	tx := &domain.OnchainTransaction{TxHash: "0xabc", ChainID: 1, Status: domain.OnchainTxStatusPending}
	pendingRepo.byHash[tx.TxHash] = tx
	chain := &fakeChain{receipts: map[common.Hash]*types.Receipt{common.HexToHash("0xabc"): {Status: 1}}}
	r := NewReconciler(chain, ChainConfig{ChainID: 1}, pendingRepo.mock)
	r.tick(context.Background())
	if tx.Status != domain.OnchainTxStatusPending {
		t.Fatalf("status should remain pending: %s", tx.Status)
	}
}

func TestReconciler_Run_StopsOnCancel(t *testing.T) {
	r := NewReconciler(&fakeChain{}, ChainConfig{}, newPendingTxStore(t).mock)
	r.Interval = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := r.Run(ctx); err == nil {
		t.Fatal("expected ctx error")
	}
}
