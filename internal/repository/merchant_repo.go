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
}

type merchantRepo struct {
	db    *sql.DB
	cache *lruCache
}

func NewMerchantRepository(db *sql.DB) MerchantRepository {
	return &merchantRepo{db: db, cache: newLRUCache(1024, 5*time.Minute)}
}

const merchantCols = `id, merchant_id, name, api_key, secret_key,
		COALESCE(callback_url,''), rate_limit, status, created_at`

func (r *merchantRepo) GetByAPIKey(ctx context.Context, apiKey string) (*domain.Merchant, error) {
	if m, ok := r.cache.get(apiKey); ok {
		return m, nil
	}
	q := `SELECT ` + merchantCols + ` FROM merchants WHERE api_key = ? LIMIT 1`
	row := r.db.QueryRowContext(ctx, q, apiKey)
	var m domain.Merchant
	var status string
	if err := row.Scan(&m.ID, &m.MerchantID, &m.Name, &m.APIKey, &m.SecretKey,
		&m.CallbackURL, &m.RateLimit, &status, &m.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrMerchantNotFound
		}
		return nil, fmt.Errorf("query merchant: %w", err)
	}
	m.Status = domain.MerchantStatus(status)
	r.cache.put(apiKey, &m)
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
