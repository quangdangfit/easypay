package blockchain

import (
	"context"
	"sync"

	"github.com/quangdangfit/easypay/internal/kafka"
	"github.com/quangdangfit/easypay/internal/repository"
)

// Listener wires the four-layer defense:
//
//	Layer 1: Subscriber (WS + cursor)
//	Layer 2: BackfillScanner (HTTP eth_getLogs)
//	Layer 3: Reconciler (receipt re-check + stuck-order alerts)
//	Layer 4: alert log lines emitted by Reconciler
//
// Each component is independently restartable; Run blocks until ctx is done
// and waits for all goroutines to exit.
type Listener struct {
	Subscriber   *Subscriber
	Backfill     *BackfillScanner
	Confirmation *ConfirmationTracker
	Reconciler   *Reconciler
}

func NewListener(
	c ChainClient,
	cfg ChainConfig,
	cursor CursorStore,
	pendingTxs repository.OnchainTxRepository,
	orders repository.OrderRepository,
	publisher kafka.EventPublisher,
) *Listener {
	return &Listener{
		Subscriber:   NewSubscriber(c, cfg, cursor, pendingTxs),
		Backfill:     NewBackfillScanner(c, cfg, cursor, pendingTxs),
		Confirmation: NewConfirmationTracker(c, cfg, pendingTxs, orders, publisher),
		Reconciler:   NewReconciler(c, cfg, pendingTxs),
	}
}

func (l *Listener) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, fn := range []func(context.Context) error{
		l.Subscriber.Run,
		l.Backfill.Run,
		l.Confirmation.Run,
		l.Reconciler.Run,
	} {
		wg.Add(1)
		go func(f func(context.Context) error) {
			defer wg.Done()
			_ = f(ctx)
		}(fn)
	}
	wg.Wait()
}
