package blockchain

import (
	"context"
	"testing"
	"time"
)

func TestListener_RunStopsAllSubLoopsOnCancel(t *testing.T) {
	chain := &fakeChain{blockNum: 0}
	cur := newMemCursor()
	repo := newPendingTxStore(t)
	orders := newTxStore(t)
	pub := newEventCapture(t)

	l := NewListener(chain, ChainConfig{ChainID: 1}, cur, repo.mock, orders.mock, pub.mock)
	// Tighten loops so cancellation is observable quickly.
	l.Backfill.Interval = 5 * time.Millisecond
	l.Confirmation.BlockTime = 5 * time.Millisecond
	l.Reconciler.Interval = 5 * time.Millisecond
	l.Subscriber.BackoffMin = 1 * time.Millisecond
	l.Subscriber.BackoffMax = 5 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after ctx cancel")
	}
}
