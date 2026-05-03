package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/quangdangfit/easypay/internal/domain"
)

var ErrMerchantNotFound = errors.New("merchant not found")

type MerchantRepository interface {
	GetByAPIKey(ctx context.Context, apiKey string) (*domain.Merchant, error)
	GetByMerchantID(ctx context.Context, merchantID string) (*domain.Merchant, error)
	// Insert persists a merchant after picking the least-loaded shard_index
	// in [0, LogicalShardCount). The picked index is written back into m.
	Insert(ctx context.Context, m *domain.Merchant) error
}

type merchantRepo struct {
	db                *sql.DB
	cache             *lruCache
	logicalShardCount uint8
}

// NewMerchantRepository builds a merchant repo whose Insert path picks
// shards in [0, logicalShardCount). The merchants table lives only on the
// control-plane pool (router.Control()); we resolve to *sql.DB at
// construction so all subsequent queries are a direct call. logicalShardCount
// must be >= 1; values of 0 are clamped up to 1 to keep the picker safe.
func NewMerchantRepository(router ShardRouter, logicalShardCount uint8) MerchantRepository {
	if logicalShardCount == 0 {
		logicalShardCount = 1
	}
	return &merchantRepo{
		db:                router.Control(),
		cache:             newLRUCache(1024, 5*time.Minute),
		logicalShardCount: logicalShardCount,
	}
}

const merchantCols = `id, merchant_id, name, api_key, secret_key,
		COALESCE(callback_url,''), rate_limit, status, shard_index, created_at`

func (r *merchantRepo) GetByAPIKey(ctx context.Context, apiKey string) (*domain.Merchant, error) {
	if m, ok := r.cache.get("api:" + apiKey); ok {
		return m, nil
	}
	q := `SELECT ` + merchantCols + ` FROM merchants WHERE api_key = ? LIMIT 1`
	row := r.db.QueryRowContext(ctx, q, apiKey)
	m, err := scanMerchant(row)
	if err != nil {
		return nil, err
	}
	r.cache.put("api:"+apiKey, m)
	return m, nil
}

func (r *merchantRepo) GetByMerchantID(ctx context.Context, merchantID string) (*domain.Merchant, error) {
	if m, ok := r.cache.get("mid:" + merchantID); ok {
		return m, nil
	}
	q := `SELECT ` + merchantCols + ` FROM merchants WHERE merchant_id = ? LIMIT 1`
	row := r.db.QueryRowContext(ctx, q, merchantID)
	m, err := scanMerchant(row)
	if err != nil {
		return nil, err
	}
	r.cache.put("mid:"+merchantID, m)
	return m, nil
}

func (r *merchantRepo) Insert(ctx context.Context, m *domain.Merchant) error {
	idx, err := r.pickLeastLoadedShard(ctx)
	if err != nil {
		return fmt.Errorf("pick shard: %w", err)
	}
	m.ShardIndex = idx
	if m.Status == "" {
		m.Status = domain.MerchantStatusActive
	}
	if m.RateLimit == 0 {
		m.RateLimit = 1000
	}
	const q = `INSERT INTO merchants
		(merchant_id, name, api_key, secret_key, callback_url, rate_limit, status, shard_index)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	res, err := r.db.ExecContext(ctx, q,
		m.MerchantID, m.Name, m.APIKey, m.SecretKey,
		nullString(m.CallbackURL), m.RateLimit, string(m.Status), m.ShardIndex,
	)
	if err != nil {
		if isDuplicateKeyErr(err) {
			return fmt.Errorf("merchant exists: %w", err)
		}
		return fmt.Errorf("insert merchant: %w", err)
	}
	if id, _ := res.LastInsertId(); id != 0 {
		m.ID = id
	}
	return nil
}

// pickLeastLoadedShard counts merchants per shard_index in
// [0, logicalShardCount) and returns the index with the smallest count.
// Empty shards (no rows) are treated as count=0 and naturally win on ties.
// On a tie the smallest index is returned (deterministic).
func (r *merchantRepo) pickLeastLoadedShard(ctx context.Context) (uint8, error) {
	const q = `SELECT shard_index, COUNT(*) FROM merchants
	            WHERE shard_index < ? GROUP BY shard_index`
	rows, err := r.db.QueryContext(ctx, q, r.logicalShardCount)
	if err != nil {
		return 0, fmt.Errorf("count by shard: %w", err)
	}
	defer func() { _ = rows.Close() }()

	counts := make([]int64, r.logicalShardCount)
	for rows.Next() {
		var idx uint8
		var n int64
		if err := rows.Scan(&idx, &n); err != nil {
			return 0, fmt.Errorf("scan shard count: %w", err)
		}
		if idx < r.logicalShardCount {
			counts[idx] = n
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate shard counts: %w", err)
	}

	best := uint8(0)
	bestN := counts[0]
	for i := uint8(1); i < r.logicalShardCount; i++ {
		if counts[i] < bestN {
			best = i
			bestN = counts[i]
		}
	}
	return best, nil
}

func scanMerchant(row *sql.Row) (*domain.Merchant, error) {
	var m domain.Merchant
	var status string
	if err := row.Scan(&m.ID, &m.MerchantID, &m.Name, &m.APIKey, &m.SecretKey,
		&m.CallbackURL, &m.RateLimit, &status, &m.ShardIndex, &m.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrMerchantNotFound
		}
		return nil, fmt.Errorf("query merchant: %w", err)
	}
	m.Status = domain.MerchantStatus(status)
	return &m, nil
}

// lruCache is a tiny in-memory cache with TTL — sufficient for hot-path
// merchant lookups. Not designed for high cardinality.
type lruCache struct {
	mu   sync.Mutex
	cap  int
	ttl  time.Duration
	data map[string]*cacheEntry
}

type cacheEntry struct {
	value     *domain.Merchant
	expiresAt time.Time
}

func newLRUCache(cap int, ttl time.Duration) *lruCache {
	return &lruCache{cap: cap, ttl: ttl, data: make(map[string]*cacheEntry, cap)}
}

func (c *lruCache) get(key string) (*domain.Merchant, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.data[key]
	if !ok || time.Now().After(e.expiresAt) {
		if ok {
			delete(c.data, key)
		}
		return nil, false
	}
	return e.value, true
}

func (c *lruCache) put(key string, m *domain.Merchant) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.data) >= c.cap {
		// O(n) eviction — fine at this size.
		var evict string
		var oldest time.Time
		for k, e := range c.data {
			if oldest.IsZero() || e.expiresAt.Before(oldest) {
				oldest = e.expiresAt
				evict = k
			}
		}
		delete(c.data, evict)
	}
	c.data[key] = &cacheEntry{value: m, expiresAt: time.Now().Add(c.ttl)}
}
