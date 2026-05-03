package repository

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/quangdangfit/easypay/internal/config"
)

// ErrUnknownShard is returned by ShardRouter.For when the supplied logical
// shard index is outside the configured logical shard count. Sharding must
// be deterministic — the routing layer never silently falls back to shard
// 0, because a wrong physical pool would scatter writes for the same
// merchant across multiple databases and break read paths.
var ErrUnknownShard = errors.New("unknown shard_index")

// ShardRouter maps a logical merchant shard_index in [0, logicalCount) to a
// physical *sql.DB pool. The mapping is fixed at startup from the config
// snapshot; rebalancing physical pools requires a deploy.
//
// The first physical pool (index 0) is the "control plane" — home of
// merchants, onchain_transactions, and block_cursors. Schema for those
// tables is also applied to non-control shards (so migrations stay
// branch-free) but they sit empty there.
type ShardRouter interface {
	// For returns the *sql.DB pool that owns logical shard `shardIndex`.
	// Returns ErrUnknownShard when shardIndex >= logicalCount.
	For(shardIndex uint8) (*sql.DB, error)
	// All returns the unique physical *sql.DB pools, in physical order
	// [0..N-1]. Used by global queries that scatter-gather (e.g.
	// reconciliation, webhook fallback by stripe_pi_id).
	All() []*sql.DB
	// Control returns the pool that owns merchants + global tables. Today
	// this is physical pool 0.
	Control() *sql.DB
	// LogicalCount returns the configured logical shard count
	// (= len(merchants.shard_index domain)). Useful for picker logic.
	LogicalCount() uint8
	// Close closes every distinct underlying pool.
	Close() error
}

// multiShardRouter is the production ShardRouter. It owns N physical pools
// and a 256-entry routing table indexed by logical shard.
type multiShardRouter struct {
	pools        []*sql.DB
	logicalToDB  []*sql.DB // len == logicalCount; nil entries past logicalCount
	logicalCount uint8
}

// OpenShards opens every configured physical pool and returns a ShardRouter.
// Falls back to a single-pool router when cfg.Shards is empty (legacy
// dev/test config that only sets cfg.DSN).
//
// `logicalCount` MUST be > 0 and divisible by len(physical pools); the
// caller (config.validate) is expected to have enforced both invariants
// already, so a violation here is a programmer error rather than a config
// bug.
func OpenShards(cfg config.DBConfig, logicalCount uint8) (ShardRouter, error) {
	if logicalCount == 0 {
		return nil, fmt.Errorf("logicalCount must be > 0")
	}

	shardCfgs := cfg.Shards
	if len(shardCfgs) == 0 {
		// Legacy single-DSN form: treat the whole cluster as one physical
		// shard. Every logical index routes to it.
		shardCfgs = []config.ShardDBConfig{{
			DSN:          cfg.DSN,
			MaxOpenConns: cfg.MaxOpenConns,
			MaxIdleConns: cfg.MaxIdleConns,
		}}
	}
	n := len(shardCfgs)
	if int(logicalCount) < n {
		return nil, fmt.Errorf("logical_shard_count (%d) < len(shards) (%d)", logicalCount, n)
	}
	if int(logicalCount)%n != 0 {
		return nil, fmt.Errorf("logical_shard_count (%d) not divisible by len(shards) (%d)", logicalCount, n)
	}

	pools := make([]*sql.DB, 0, n)
	for i, sc := range shardCfgs {
		open := sc.MaxOpenConns
		if open == 0 {
			open = cfg.MaxOpenConns
		}
		idle := sc.MaxIdleConns
		if idle == 0 {
			idle = cfg.MaxIdleConns
		}
		db, err := openPool(sc.DSN, open, idle)
		if err != nil {
			// Roll back any pools we've already opened so we don't leak
			// connections on a partial failure.
			for _, p := range pools {
				_ = p.Close()
			}
			return nil, fmt.Errorf("shard %d: %w", i, err)
		}
		pools = append(pools, db)
	}

	// Range-partition logical → physical. With L=16 logical and P=2
	// physical, logical 0..7 → pool 0 and 8..15 → pool 1. Cheap O(1)
	// lookup at runtime via a precomputed table.
	logicalToDB := make([]*sql.DB, logicalCount)
	for i := uint8(0); i < logicalCount; i++ {
		physical := int(i) * n / int(logicalCount)
		logicalToDB[i] = pools[physical]
	}

	return &multiShardRouter{
		pools:        pools,
		logicalToDB:  logicalToDB,
		logicalCount: logicalCount,
	}, nil
}

func (r *multiShardRouter) For(shardIndex uint8) (*sql.DB, error) {
	if shardIndex >= r.logicalCount {
		return nil, fmt.Errorf("%w: %d (logical_shard_count=%d)", ErrUnknownShard, shardIndex, r.logicalCount)
	}
	return r.logicalToDB[shardIndex], nil
}

func (r *multiShardRouter) All() []*sql.DB {
	// Defensive copy so callers can't mutate the underlying slice.
	out := make([]*sql.DB, len(r.pools))
	copy(out, r.pools)
	return out
}

func (r *multiShardRouter) Control() *sql.DB { return r.pools[0] }

func (r *multiShardRouter) LogicalCount() uint8 { return r.logicalCount }

func (r *multiShardRouter) Close() error {
	var firstErr error
	for _, db := range r.pools {
		if err := db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// NewSingleShardRouter wraps an already-open *sql.DB as a one-pool router.
// Used by integration tests that bring up a single MySQL container and by
// any caller that wants to share an existing pool. logicalCount defaults
// to 1 when zero.
func NewSingleShardRouter(db *sql.DB, logicalCount uint8) ShardRouter {
	if logicalCount == 0 {
		logicalCount = 1
	}
	logicalToDB := make([]*sql.DB, logicalCount)
	for i := range logicalToDB {
		logicalToDB[i] = db
	}
	return &multiShardRouter{
		pools:        []*sql.DB{db},
		logicalToDB:  logicalToDB,
		logicalCount: logicalCount,
	}
}
