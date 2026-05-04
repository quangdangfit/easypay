package blockchain

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/quangdangfit/easypay/internal/domain"
)

func receiptOK() *types.Receipt {
	return &types.Receipt{Status: 1, BlockNumber: big.NewInt(50)}
}

func TestConfirmation_ProcessOne_ConfirmsAndPublishes(t *testing.T) {
	pendingRepo := newPendingTxStore(t)
	tx := &domain.OnchainTransaction{
		TxHash: "0xabc", BlockNumber: 50, OrderID: "ord-1", ChainID: 1,
		RequiredConfirm: 12, Amount: big.NewInt(1500), Status: domain.OnchainTxStatusPending,
	}
	pendingRepo.byHash[tx.TxHash] = tx
	chain := &fakeChain{receipts: map[common.Hash]*types.Receipt{common.HexToHash("0xabc"): receiptOK()}}
	orders := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Status: domain.TransactionStatusPending})
	pub := newEventCapture(t)
	tracker := NewConfirmationTracker(chain, ChainConfig{ChainID: 1, RequiredConfirmations: 12}, pendingRepo.mock, orders.mock, pub.mock)
	tracker.processOne(context.Background(), tx, 100) // 100-50 = 50 >= 12 confirmations

	if tx.Status != domain.OnchainTxStatusConfirmed {
		t.Fatalf("status: %s", tx.Status)
	}
	if orders.byID["ord-1"].Status != domain.TransactionStatusPaid {
		t.Fatalf("order: %s", orders.byID["ord-1"].Status)
	}
	if len(pub.confirmed) != 1 {
		t.Fatalf("confirmations: %d", len(pub.confirmed))
	}
}

func TestConfirmation_ProcessOne_ReorgedOnMissingReceipt(t *testing.T) {
	pendingRepo := newPendingTxStore(t)
	tx := &domain.OnchainTransaction{TxHash: "0xabc", Status: domain.OnchainTxStatusPending}
	pendingRepo.byHash[tx.TxHash] = tx
	chain := &fakeChain{receiptErr: errors.New("not found")}
	tracker := NewConfirmationTracker(chain, ChainConfig{}, pendingRepo.mock, newTxStore(t).mock, newEventCapture(t).mock)
	tracker.processOne(context.Background(), tx, 100)
	if tx.Status != domain.OnchainTxStatusReorged {
		t.Fatalf("status: %s", tx.Status)
	}
}

func TestConfirmation_ProcessOne_FailedReceipt(t *testing.T) {
	pendingRepo := newPendingTxStore(t)
	tx := &domain.OnchainTransaction{TxHash: "0xabc", Status: domain.OnchainTxStatusPending}
	pendingRepo.byHash[tx.TxHash] = tx
	chain := &fakeChain{receipts: map[common.Hash]*types.Receipt{common.HexToHash("0xabc"): {Status: 0}}}
	tracker := NewConfirmationTracker(chain, ChainConfig{}, pendingRepo.mock, newTxStore(t).mock, newEventCapture(t).mock)
	tracker.processOne(context.Background(), tx, 100)
	if tx.Status != domain.OnchainTxStatusFailed {
		t.Fatalf("status: %s", tx.Status)
	}
}

func TestConfirmation_ProcessOne_NotEnoughConfirmations(t *testing.T) {
	pendingRepo := newPendingTxStore(t)
	tx := &domain.OnchainTransaction{
		TxHash: "0xabc", BlockNumber: 95, RequiredConfirm: 12, ChainID: 1,
		Status: domain.OnchainTxStatusPending,
	}
	pendingRepo.byHash[tx.TxHash] = tx
	chain := &fakeChain{receipts: map[common.Hash]*types.Receipt{common.HexToHash("0xabc"): receiptOK()}}
	tracker := NewConfirmationTracker(chain, ChainConfig{ChainID: 1}, pendingRepo.mock, newTxStore(t).mock, newEventCapture(t).mock)
	tracker.processOne(context.Background(), tx, 100) // 5 confirmations
	if tx.Status != domain.OnchainTxStatusPending {
		t.Fatalf("status should stay pending: %s", tx.Status)
	}
	if tx.Confirmations != 5 {
		t.Fatalf("confirmations: %d", tx.Confirmations)
	}
}

func TestConfirmation_ProcessOne_AmountMismatch(t *testing.T) {
	pendingRepo := newPendingTxStore(t)
	tx := &domain.OnchainTransaction{
		TxHash: "0xabc", BlockNumber: 50, RequiredConfirm: 12, OrderID: "ord-1",
		Amount: big.NewInt(999), Status: domain.OnchainTxStatusPending,
	}
	pendingRepo.byHash[tx.TxHash] = tx
	chain := &fakeChain{receipts: map[common.Hash]*types.Receipt{common.HexToHash("0xabc"): receiptOK()}}
	orders := newTxStore(t, &domain.Transaction{OrderID: "ord-1", Amount: 1500, Status: domain.TransactionStatusPending})
	tracker := NewConfirmationTracker(chain, ChainConfig{}, pendingRepo.mock, orders.mock, newEventCapture(t).mock)
	tracker.processOne(context.Background(), tx, 100)
	if tx.Status != domain.OnchainTxStatusFailed {
		t.Fatalf("status: %s", tx.Status)
	}
}

func TestConfirmation_ProcessOne_AlreadyFinalised(t *testing.T) {
	pendingRepo := newPendingTxStore(t)
	tx := &domain.OnchainTransaction{
		TxHash: "0xabc", BlockNumber: 50, RequiredConfirm: 12, OrderID: "ord-1",
		Amount: big.NewInt(1500), Status: domain.OnchainTxStatusPending,
	}
	pendingRepo.byHash[tx.TxHash] = tx
	chain := &fakeChain{receipts: map[common.Hash]*types.Receipt{common.HexToHash("0xabc"): receiptOK()}}
	orders := newTxStore(t, &domain.Transaction{OrderID: "ord-1", Amount: 1500, Status: domain.TransactionStatusPaid})
	pub := newEventCapture(t)
	tracker := NewConfirmationTracker(chain, ChainConfig{}, pendingRepo.mock, orders.mock, pub.mock)
	tracker.processOne(context.Background(), tx, 100)
	if tx.Status != domain.OnchainTxStatusConfirmed {
		t.Fatalf("status: %s", tx.Status)
	}
	if len(pub.confirmed) != 0 {
		t.Fatal("should not republish for already-paid order")
	}
}

func TestConfirmation_Tick_BlockNumberError(t *testing.T) {
	chain := &fakeChain{blockNumErr: errors.New("rpc")}
	tracker := NewConfirmationTracker(chain, ChainConfig{}, newPendingTxStore(t).mock, newTxStore(t).mock, newEventCapture(t).mock)
	if err := tracker.tick(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestConfirmation_Run_StopsOnCancel(t *testing.T) {
	tracker := NewConfirmationTracker(&fakeChain{blockNum: 100}, ChainConfig{ChainID: 1}, newPendingTxStore(t).mock, newTxStore(t).mock, newEventCapture(t).mock)
	tracker.BlockTime = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := tracker.Run(ctx); err == nil {
		t.Fatal("expected ctx error")
	}
}
