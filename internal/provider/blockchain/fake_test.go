package blockchain

import (
	"context"
	"errors"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// fakeChain is a hand-rolled stand-in for ChainClient. Tests configure each
// behaviour explicitly so we cover branches without spinning up an RPC.
type fakeChain struct {
	blockNum    uint64
	blockNumErr error
	subErr      error
	logs        []types.Log
	filterErr   error
	receipts    map[common.Hash]*types.Receipt
	receiptErr  error
	subscribed  bool
	logCh       chan<- types.Log
	closed      bool
}

func (f *fakeChain) BlockNumber(ctx context.Context) (uint64, error) {
	return f.blockNum, f.blockNumErr
}

func (f *fakeChain) SubscribeFilterLogs(ctx context.Context, q ethereum.FilterQuery, ch chan<- types.Log) (ethereum.Subscription, error) {
	if f.subErr != nil {
		return nil, f.subErr
	}
	f.subscribed = true
	f.logCh = ch
	return &fakeSub{errCh: make(chan error, 1)}, nil
}

func (f *fakeChain) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	return f.logs, f.filterErr
}

func (f *fakeChain) TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error) {
	if f.receiptErr != nil {
		return nil, f.receiptErr
	}
	if r, ok := f.receipts[hash]; ok {
		return r, nil
	}
	return nil, errors.New("receipt not found")
}

func (f *fakeChain) Close() { f.closed = true }

type fakeSub struct{ errCh chan error }

func (s *fakeSub) Err() <-chan error { return s.errCh }
func (s *fakeSub) Unsubscribe()      { close(s.errCh) }
