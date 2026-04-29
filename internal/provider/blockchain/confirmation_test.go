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
	"github.com/quangdangfit/easypay/internal/kafka"
)

type fakeOrders struct {
	byID map[string]*domain.Order
}

func (f *fakeOrders) Create(context.Context, *domain.Order) error { return nil }
func (f *fakeOrders) GetByOrderID(_ context.Context, id string) (*domain.Order, error) {
	if o, ok := f.byID[id]; ok {
		return o, nil
	}
	return nil, errors.New("not found")
}
func (f *fakeOrders) GetByPaymentIntentID(context.Context, string) (*domain.Order, error) {
	return nil, errors.New("nf")
}
func (f *fakeOrders) UpdateStatus(_ context.Context, id string, st domain.OrderStatus, _ string) error {
	if o, ok := f.byID[id]; ok {
		o.Status = st
		return nil
	}
	return errors.New("nf")
}
func (f *fakeOrders) UpdateCheckout(context.Context, string, string, string, string) error {
	return nil
}
func (f *fakeOrders) BatchCreate(context.Context, []*domain.Order) error { return nil }
func (f *fakeOrders) GetPendingBefore(context.Context, time.Time, int) ([]*domain.Order, error) {
	return nil, nil
}

type fakePub struct {
	confirmed []kafka.PaymentConfirmedEvent
}

func (p *fakePub) PublishPaymentEvent(context.Context, kafka.PaymentEvent) error { return nil }
func (p *fakePub) PublishPaymentConfirmed(_ context.Context, e kafka.PaymentConfirmedEvent) error {
	p.confirmed = append(p.confirmed, e)
	return nil
}
func (p *fakePub) Close() error { return nil }

func receiptOK() *types.Receipt {
	return &types.Receipt{Status: 1, BlockNumber: big.NewInt(50)}
}

func TestConfirmation_ProcessOne_ConfirmsAndPublishes(t *testing.T) {
	pendingRepo := newMemPendingTx()
	tx := &domain.PendingTx{
		TxHash: "0xabc", BlockNumber: 50, OrderID: "ORD-1", ChainID: 1,
		RequiredConfirm: 12, Amount: big.NewInt(1500), Status: domain.PendingTxStatusPending,
	}
	pendingRepo.byHash[tx.TxHash] = tx
	chain := &fakeChain{receipts: map[common.Hash]*types.Receipt{common.HexToHash("0xabc"): receiptOK()}}
	orders := &fakeOrders{byID: map[string]*domain.Order{
		"ORD-1": {OrderID: "ORD-1", MerchantID: "M1", Amount: 1500, Status: domain.OrderStatusPending},
	}}
	pub := &fakePub{}
	tracker := NewConfirmationTracker(chain, ChainConfig{ChainID: 1, RequiredConfirmations: 12}, pendingRepo, orders, pub)
	tracker.processOne(context.Background(), tx, 100) // 100-50 = 50 >= 12 confirmations

	if tx.Status != domain.PendingTxStatusConfirmed {
		t.Fatalf("status: %s", tx.Status)
	}
	if orders.byID["ORD-1"].Status != domain.OrderStatusPaid {
		t.Fatalf("order: %s", orders.byID["ORD-1"].Status)
	}
	if len(pub.confirmed) != 1 {
		t.Fatalf("confirmations: %d", len(pub.confirmed))
	}
}

func TestConfirmation_ProcessOne_ReorgedOnMissingReceipt(t *testing.T) {
	pendingRepo := newMemPendingTx()
	tx := &domain.PendingTx{TxHash: "0xabc", Status: domain.PendingTxStatusPending}
	pendingRepo.byHash[tx.TxHash] = tx
	chain := &fakeChain{receiptErr: errors.New("not found")}
	tracker := NewConfirmationTracker(chain, ChainConfig{}, pendingRepo, &fakeOrders{}, &fakePub{})
	tracker.processOne(context.Background(), tx, 100)
	if tx.Status != domain.PendingTxStatusReorged {
		t.Fatalf("status: %s", tx.Status)
	}
}

func TestConfirmation_ProcessOne_FailedReceipt(t *testing.T) {
	pendingRepo := newMemPendingTx()
	tx := &domain.PendingTx{TxHash: "0xabc", Status: domain.PendingTxStatusPending}
	pendingRepo.byHash[tx.TxHash] = tx
	chain := &fakeChain{receipts: map[common.Hash]*types.Receipt{common.HexToHash("0xabc"): {Status: 0}}}
	tracker := NewConfirmationTracker(chain, ChainConfig{}, pendingRepo, &fakeOrders{}, &fakePub{})
	tracker.processOne(context.Background(), tx, 100)
	if tx.Status != domain.PendingTxStatusFailed {
		t.Fatalf("status: %s", tx.Status)
	}
}

func TestConfirmation_ProcessOne_NotEnoughConfirmations(t *testing.T) {
	pendingRepo := newMemPendingTx()
	tx := &domain.PendingTx{
		TxHash: "0xabc", BlockNumber: 95, RequiredConfirm: 12, ChainID: 1,
		Status: domain.PendingTxStatusPending,
	}
	pendingRepo.byHash[tx.TxHash] = tx
	chain := &fakeChain{receipts: map[common.Hash]*types.Receipt{common.HexToHash("0xabc"): receiptOK()}}
	tracker := NewConfirmationTracker(chain, ChainConfig{ChainID: 1}, pendingRepo, &fakeOrders{}, &fakePub{})
	tracker.processOne(context.Background(), tx, 100) // 5 confirmations
	if tx.Status != domain.PendingTxStatusPending {
		t.Fatalf("status should stay pending: %s", tx.Status)
	}
	if tx.Confirmations != 5 {
		t.Fatalf("confirmations: %d", tx.Confirmations)
	}
}

func TestConfirmation_ProcessOne_AmountMismatch(t *testing.T) {
	pendingRepo := newMemPendingTx()
	tx := &domain.PendingTx{
		TxHash: "0xabc", BlockNumber: 50, RequiredConfirm: 12, OrderID: "ORD-1",
		Amount: big.NewInt(999), Status: domain.PendingTxStatusPending,
	}
	pendingRepo.byHash[tx.TxHash] = tx
	chain := &fakeChain{receipts: map[common.Hash]*types.Receipt{common.HexToHash("0xabc"): receiptOK()}}
	orders := &fakeOrders{byID: map[string]*domain.Order{
		"ORD-1": {OrderID: "ORD-1", Amount: 1500, Status: domain.OrderStatusPending},
	}}
	tracker := NewConfirmationTracker(chain, ChainConfig{}, pendingRepo, orders, &fakePub{})
	tracker.processOne(context.Background(), tx, 100)
	if tx.Status != domain.PendingTxStatusFailed {
		t.Fatalf("status: %s", tx.Status)
	}
}

func TestConfirmation_ProcessOne_AlreadyFinalised(t *testing.T) {
	pendingRepo := newMemPendingTx()
	tx := &domain.PendingTx{
		TxHash: "0xabc", BlockNumber: 50, RequiredConfirm: 12, OrderID: "ORD-1",
		Amount: big.NewInt(1500), Status: domain.PendingTxStatusPending,
	}
	pendingRepo.byHash[tx.TxHash] = tx
	chain := &fakeChain{receipts: map[common.Hash]*types.Receipt{common.HexToHash("0xabc"): receiptOK()}}
	orders := &fakeOrders{byID: map[string]*domain.Order{
		"ORD-1": {OrderID: "ORD-1", Amount: 1500, Status: domain.OrderStatusPaid},
	}}
	pub := &fakePub{}
	tracker := NewConfirmationTracker(chain, ChainConfig{}, pendingRepo, orders, pub)
	tracker.processOne(context.Background(), tx, 100)
	if tx.Status != domain.PendingTxStatusConfirmed {
		t.Fatalf("status: %s", tx.Status)
	}
	if len(pub.confirmed) != 0 {
		t.Fatal("should not republish for already-paid order")
	}
}

func TestConfirmation_Tick_BlockNumberError(t *testing.T) {
	chain := &fakeChain{blockNumErr: errors.New("rpc")}
	tracker := NewConfirmationTracker(chain, ChainConfig{}, newMemPendingTx(), &fakeOrders{}, &fakePub{})
	if err := tracker.tick(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestConfirmation_Run_StopsOnCancel(t *testing.T) {
	tracker := NewConfirmationTracker(&fakeChain{blockNum: 100}, ChainConfig{ChainID: 1}, newMemPendingTx(), &fakeOrders{}, &fakePub{})
	tracker.BlockTime = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := tracker.Run(ctx); err == nil {
		t.Fatal("expected ctx error")
	}
}
