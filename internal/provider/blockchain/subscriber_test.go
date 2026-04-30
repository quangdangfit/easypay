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

func sampleLog() types.Log {
	orderHex := common.LeftPadBytes([]byte("ord-1"), 32)
	payer := common.HexToAddress("0xdEAD000000000000000000000000000000000001")
	token := common.HexToAddress("0xC0FFEE0000000000000000000000000000000002")
	data := make([]byte, 64)
	copy(data[12:32], token.Bytes())
	amt := new(big.Int).SetUint64(1500)
	amtBytes := amt.Bytes()
	copy(data[64-len(amtBytes):], amtBytes)

	return types.Log{
		TxHash:      common.HexToHash("0xabc"),
		BlockNumber: 100,
		Topics: []common.Hash{
			common.HexToHash("0xeventsig"),
			common.BytesToHash(orderHex),
			common.BytesToHash(payer.Bytes()),
		},
		Data: data,
	}
}

func TestSubscriber_HandleLog_PersistsAndAdvancesCursor(t *testing.T) {
	chain := &fakeChain{}
	cur := newMemCursor()
	repo := newPendingTxStore(t)
	cfg := ChainConfig{ChainID: 1, ContractAddress: common.HexToAddress("0xC0NTRACT"), RequiredConfirmations: 12}
	s := NewSubscriber(chain, cfg, cur, repo.mock)

	if err := s.handleLog(context.Background(), sampleLog()); err != nil {
		t.Fatalf("handleLog: %v", err)
	}
	if len(repo.byHash) != 1 {
		t.Fatalf("expected 1 pending tx, got %d", len(repo.byHash))
	}
	if cur.v[1] != 100 {
		t.Fatalf("cursor: %d", cur.v[1])
	}
}

func TestSubscriber_HandleLog_DuplicateIsNoOp(t *testing.T) {
	chain := &fakeChain{}
	cur := newMemCursor()
	repo := newPendingTxStore(t)
	repo.byHash["0x000000000000000000000000000000000000000000000000000000000000abc"] = &domain.PendingTx{}

	s := NewSubscriber(chain, ChainConfig{ChainID: 1}, cur, repo.mock)
	if err := s.handleLog(context.Background(), sampleLog()); err != nil {
		t.Fatalf("dup should be no-op: %v", err)
	}
}

func TestSubscriber_HandleLog_BadEventReturnsError(t *testing.T) {
	chain := &fakeChain{}
	s := NewSubscriber(chain, ChainConfig{ChainID: 1}, newMemCursor(), newPendingTxStore(t).mock)
	bad := types.Log{TxHash: common.HexToHash("0xdead")} // empty topics → ParsePaymentEvent fails
	if err := s.handleLog(context.Background(), bad); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestSubscriber_RunOnce_Subscribes(t *testing.T) {
	chain := &fakeChain{}
	s := NewSubscriber(chain, ChainConfig{ChainID: 1}, newMemCursor(), newPendingTxStore(t).mock)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := s.runOnce(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if !chain.subscribed {
		t.Fatal("expected SubscribeFilterLogs to have been called")
	}
}

func TestSubscriber_RunOnce_SubscribeError(t *testing.T) {
	chain := &fakeChain{subErr: errors.New("ws failed")}
	s := NewSubscriber(chain, ChainConfig{ChainID: 1}, newMemCursor(), newPendingTxStore(t).mock)
	if err := s.runOnce(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestSubscriber_Run_StopsOnContextCancel(t *testing.T) {
	chain := &fakeChain{}
	s := NewSubscriber(chain, ChainConfig{ChainID: 1}, newMemCursor(), newPendingTxStore(t).mock)
	s.BackoffMin = time.Millisecond
	s.BackoffMax = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := s.Run(ctx); !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context error, got %v", err)
	}
}

func TestDoubleCapped(t *testing.T) {
	if got := doubleCapped(time.Second, 3*time.Second); got != 2*time.Second {
		t.Errorf("got %v", got)
	}
	if got := doubleCapped(2*time.Second, 3*time.Second); got != 3*time.Second {
		t.Errorf("cap not respected: %v", got)
	}
}
