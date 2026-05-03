// Bench is a self-contained load generator for the easypay API. It seeds
// one or more test merchants directly into MySQL, then floods POST
// /api/payments (and optionally GET /api/payments/:id) with HMAC-signed
// requests. Workers pick a merchant uniformly at random per request, so
// running with --merchants N exercises N logical shards (and therefore
// every physical pool that owns at least one of those shards). At the
// end it prints throughput, error counts, latency percentiles, and the
// per-shard request distribution.
//
// Examples:
//
//	# single merchant, legacy default
//	go run ./cmd/bench --url http://localhost:8080 --concurrency 100 --total 10000
//
//	# 32 merchants, picked into shards by the same least-loaded picker the
//	# server uses, against a multi-shard config:
//	go run ./cmd/bench --config config.yaml --merchants 32 --duration 60s
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"

	appconfig "github.com/quangdangfit/easypay/internal/config"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/pkg/hmac"
)

const defaultLogicalShardCount uint8 = 16

type config struct {
	url               string
	dbDSN             string
	configPath        string
	concurrency       int
	total             int
	duration          time.Duration
	merchantID        string
	apiKey            string
	secretKey         string
	method            string
	pollStatus        bool
	timeout           time.Duration
	merchants         int
	logicalShardCount int
}

// benchMerchant is one of the merchants the bench seeded. Workers pick
// from a slice of these per request.
type benchMerchant struct {
	id         string
	apiKey     string
	secretKey  string
	shardIndex uint8
}

func main() {
	cfg := parseFlags()
	if err := run(cfg); err != nil {
		log.Fatalf("bench: %v", err)
	}
}

func parseFlags() config {
	cfg := config{
		url:         "http://localhost:8080",
		dbDSN:       os.Getenv("DB_DSN"),
		concurrency: 50,
		total:       1000,
		duration:    0,
		merchantID:  "BENCH_M1",
		apiKey:      "bench-api-key",
		secretKey:   "bench-secret-key-32-bytes-minimum-len",
		method:      "card",
		pollStatus:  false,
		timeout:     10 * time.Second,
		merchants:   1,
	}
	flag.StringVar(&cfg.url, "url", cfg.url, "base URL of the easypay server")
	flag.StringVar(&cfg.configPath, "config", "", "YAML config path; takes precedence over --db-dsn and uses the control-plane DSN to seed the merchant")
	flag.StringVar(&cfg.dbDSN, "db-dsn", cfg.dbDSN, "MySQL DSN for merchant seeding (defaults to DB_DSN env). Ignored when --config is set.")
	flag.IntVar(&cfg.concurrency, "concurrency", cfg.concurrency, "number of concurrent workers")
	flag.IntVar(&cfg.total, "total", cfg.total, "total requests (ignored if --duration > 0)")
	flag.DurationVar(&cfg.duration, "duration", cfg.duration, "run for this long instead of --total")
	flag.StringVar(&cfg.merchantID, "merchant-id", cfg.merchantID, "merchant id (base; suffixed _0,_1,... when --merchants > 1)")
	flag.StringVar(&cfg.apiKey, "api-key", cfg.apiKey, "merchant api key (base; suffixed when --merchants > 1)")
	flag.StringVar(&cfg.secretKey, "secret-key", cfg.secretKey, "merchant secret key (base; suffixed when --merchants > 1)")
	flag.StringVar(&cfg.method, "method", cfg.method, "payment method: card or crypto")
	flag.BoolVar(&cfg.pollStatus, "poll-status", cfg.pollStatus, "after each create, GET /api/payments/:id once")
	flag.DurationVar(&cfg.timeout, "timeout", cfg.timeout, "per-request HTTP timeout")
	flag.IntVar(&cfg.merchants, "merchants", cfg.merchants, "number of distinct merchants to seed and round-robin (>= 1). Use >1 to exercise multiple shards.")
	flag.IntVar(&cfg.logicalShardCount, "logical-shard-count", 0, "logical shard count to use when seeding. Defaults to the value in --config, or 16.")
	flag.Parse()
	if cfg.merchants < 1 {
		cfg.merchants = 1
	}
	return cfg
}

func run(cfg config) error {
	settings, err := resolveSeedSettings(cfg)
	if err != nil {
		return err
	}

	ctx := context.Background()
	merchants, err := seedMerchants(ctx, cfg, settings.dsn, settings.logicalShardCount)
	if err != nil {
		return fmt.Errorf("seed merchants: %w", err)
	}
	printSeedReport(merchants, settings.logicalShardCount)

	httpClient := &http.Client{
		Timeout: cfg.timeout,
		Transport: &http.Transport{
			MaxIdleConns:        cfg.concurrency * 2,
			MaxIdleConnsPerHost: cfg.concurrency * 2,
			IdleConnTimeout:     30 * time.Second,
		},
	}

	type result struct {
		status     int
		latency    time.Duration
		err        error
		orderID    string
		shardIndex uint8
	}

	results := make(chan result, cfg.concurrency*2)
	var stop atomic.Bool
	var runCtx context.Context
	var cancel context.CancelFunc
	if cfg.duration > 0 {
		runCtx, cancel = context.WithTimeout(ctx, cfg.duration)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	var sent atomic.Int64
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(rand.Int())))
		for {
			if cfg.duration == 0 && sent.Load() >= int64(cfg.total) {
				return
			}
			if stop.Load() || runCtx.Err() != nil {
				return
			}
			sent.Add(1)
			m := &merchants[rng.Intn(len(merchants))]
			start := time.Now()
			orderID, status, err := doCreate(runCtx, httpClient, cfg, m, rng)
			results <- result{
				status:     status,
				latency:    time.Since(start),
				err:        err,
				orderID:    orderID,
				shardIndex: m.shardIndex,
			}

			if cfg.pollStatus && err == nil && orderID != "" {
				start := time.Now()
				st, err := doGet(runCtx, httpClient, m, cfg.url, orderID)
				results <- result{
					status:     st,
					latency:    time.Since(start),
					err:        err,
					shardIndex: m.shardIndex,
				}
			}
		}
	}

	t0 := time.Now()
	for i := 0; i < cfg.concurrency; i++ {
		wg.Add(1)
		go worker()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	stats := newStats()
	progressEvery := time.Second
	progressTick := time.NewTicker(progressEvery)
	defer progressTick.Stop()
	last := stats.snapshot()

	done := false
	for !done {
		select {
		case r, ok := <-results:
			if !ok {
				done = true
				break
			}
			stats.add(r.latency, r.status, r.err, r.shardIndex)
		case <-progressTick.C:
			cur := stats.snapshot()
			fmt.Fprintf(os.Stderr, "[%s]  total=%d  ok=%d  err=%d  inst=%.0f rps\n",
				time.Since(t0).Round(time.Second),
				cur.total, cur.ok, cur.err,
				float64(cur.total-last.total)/progressEvery.Seconds(),
			)
			last = cur
		case <-runCtx.Done():
			stop.Store(true)
		}
	}

	stats.report(time.Since(t0))
	return nil
}

func doCreate(ctx context.Context, c *http.Client, cfg config, m *benchMerchant, rng *rand.Rand) (orderID string, status int, err error) {
	body := map[string]any{
		"order_id":             fmt.Sprintf("BENCH-%d-%s", time.Now().UnixNano(), uuid.NewString()[:8]),
		"amount":               int64(100 + rng.Intn(99900)), // $1.00 - $1000
		"currency":             "USD",
		"payment_method_types": []string{"card"},
		"customer_email":       fmt.Sprintf("user%d@bench.local", rng.Intn(10000)),
		"success_url":          "https://bench.local/success",
		"cancel_url":           "https://bench.local/cancel",
	}
	payload, _ := json.Marshal(body)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	signed := append([]byte(ts+"."), payload...)
	sig := hmac.Sign(m.secretKey, signed)

	url := cfg.url + "/api/payments"
	if cfg.method == "crypto" {
		url += "?method=crypto"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", m.apiKey)
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Signature", sig)

	resp, err := c.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	status = resp.StatusCode
	if status >= 200 && status < 300 {
		var env struct {
			Data struct {
				OrderID string `json:"order_id"`
			} `json:"data"`
		}
		if json.Unmarshal(rb, &env) == nil {
			orderID = env.Data.OrderID
		}
		return orderID, status, nil
	}
	return "", status, fmt.Errorf("status %d: %s", status, truncate(string(rb), 200))
}

func doGet(ctx context.Context, c *http.Client, m *benchMerchant, baseURL, orderID string) (int, error) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := hmac.Sign(m.secretKey, []byte(ts+"."))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/payments/"+orderID, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-API-Key", m.apiKey)
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Signature", sig)
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("status %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// seedSettings carry the values resolved from --config / --db-dsn / flags
// that we need for merchant seeding.
type seedSettings struct {
	dsn               string
	logicalShardCount uint8
}

// resolveSeedSettings picks the DSN we'll use to seed the bench merchants
// and the logical shard count to drive the picker.
//
// When --config is set we load the same YAML the server uses and take the
// control-plane DSN (db.shards[0] when shards are configured, otherwise
// db.dsn) plus app.logical_shard_count. This keeps bench in lockstep with
// the server's view of where the merchants table lives — we don't want to
// seed against db1 while the gateway looks up merchants on db0.
//
// Without --config we honour the legacy --db-dsn / DB_DSN env path; the
// shard count comes from --logical-shard-count or defaults to 16, which
// must match the server.
func resolveSeedSettings(cfg config) (seedSettings, error) {
	logicalCount := defaultLogicalShardCount
	if cfg.logicalShardCount > 0 {
		logicalCount = clampShardCount(cfg.logicalShardCount)
	}

	if cfg.configPath == "" {
		if cfg.dbDSN == "" {
			return seedSettings{}, fmt.Errorf("DB_DSN not set; pass --config, --db-dsn, or set DB_DSN env")
		}
		return seedSettings{dsn: cfg.dbDSN, logicalShardCount: logicalCount}, nil
	}

	app, err := appconfig.Load(cfg.configPath)
	if err != nil {
		return seedSettings{}, fmt.Errorf("load config %s: %w", cfg.configPath, err)
	}
	dsn := ""
	switch {
	case len(app.DB.Shards) > 0:
		dsn = app.DB.Shards[0].DSN
	case app.DB.DSN != "":
		dsn = app.DB.DSN
	default:
		return seedSettings{}, fmt.Errorf("config %s has neither db.shards nor db.dsn", cfg.configPath)
	}
	// Flag overrides config; otherwise inherit from YAML.
	if cfg.logicalShardCount == 0 && app.App.LogicalShardCount > 0 {
		logicalCount = app.App.LogicalShardCount
	}
	return seedSettings{dsn: dsn, logicalShardCount: logicalCount}, nil
}

func clampShardCount(v int) uint8 {
	if v < 1 {
		return 1
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// seedMerchants ensures cfg.merchants merchants exist with the bench
// credentials. New merchants are inserted via the same merchant repo the
// server uses, so they hit the least-loaded shard picker; existing
// merchants keep their assigned shard but have their credentials
// refreshed in case the bench flags changed between runs.
func seedMerchants(ctx context.Context, cfg config, dsn string, logicalCount uint8) ([]benchMerchant, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	router := repository.NewSingleShardRouter(db, logicalCount)
	repo := repository.NewMerchantRepository(router, logicalCount)

	out := make([]benchMerchant, cfg.merchants)
	for i := 0; i < cfg.merchants; i++ {
		bm := mintBenchMerchant(cfg, i)
		seedCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		existing, err := repo.GetByMerchantID(seedCtx, bm.id)
		cancel()
		switch {
		case err == nil:
			refreshCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, uerr := db.ExecContext(refreshCtx, `UPDATE merchants
			    SET api_key = ?, secret_key = ?, rate_limit = ?, status = 'active'
			    WHERE merchant_id = ?`,
				bm.apiKey, bm.secretKey, 1_000_000, bm.id)
			cancel()
			if uerr != nil {
				return nil, fmt.Errorf("refresh merchant %s: %w", bm.id, uerr)
			}
			bm.shardIndex = existing.ShardIndex
		case errors.Is(err, repository.ErrMerchantNotFound):
			m := &domain.Merchant{
				MerchantID: bm.id,
				Name:       "Bench Merchant " + bm.id,
				APIKey:     bm.apiKey,
				SecretKey:  bm.secretKey,
				RateLimit:  1_000_000,
				Status:     domain.MerchantStatusActive,
			}
			insCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			ierr := repo.Insert(insCtx, m)
			cancel()
			if ierr != nil {
				return nil, fmt.Errorf("insert merchant %s: %w", bm.id, ierr)
			}
			bm.shardIndex = m.ShardIndex
		default:
			return nil, fmt.Errorf("lookup merchant %s: %w", bm.id, err)
		}
		out[i] = bm
	}
	return out, nil
}

// mintBenchMerchant produces the i-th bench merchant's credentials. With
// --merchants 1 we keep the bare flag values for backward compatibility;
// with N > 1 we suffix each field so the resulting (merchant_id, api_key)
// rows are all unique.
func mintBenchMerchant(cfg config, i int) benchMerchant {
	if cfg.merchants == 1 {
		return benchMerchant{
			id:        cfg.merchantID,
			apiKey:    cfg.apiKey,
			secretKey: cfg.secretKey,
		}
	}
	return benchMerchant{
		id:        fmt.Sprintf("%s_%d", cfg.merchantID, i),
		apiKey:    fmt.Sprintf("%s-%d", cfg.apiKey, i),
		secretKey: fmt.Sprintf("%s-%d", cfg.secretKey, i),
	}
}

func printSeedReport(merchants []benchMerchant, logicalCount uint8) {
	perShard := map[uint8]int{}
	for _, m := range merchants {
		perShard[m.shardIndex]++
	}
	shards := make([]uint8, 0, len(perShard))
	for s := range perShard {
		shards = append(shards, s)
	}
	sort.Slice(shards, func(i, j int) bool { return shards[i] < shards[j] })
	fmt.Fprintf(os.Stderr, "seeded %d merchant(s) across %d/%d logical shard(s):\n",
		len(merchants), len(perShard), logicalCount)
	for _, s := range shards {
		fmt.Fprintf(os.Stderr, "  shard %3d: %d merchant(s)\n", s, perShard[s])
	}
}

// stats accumulates results for end-of-run reporting.
type stats struct {
	mu        sync.Mutex
	latencies []time.Duration
	statusCnt map[int]int
	shardCnt  map[uint8]int
	errCnt    int
	okCnt     int
}

func newStats() *stats {
	return &stats{
		statusCnt: map[int]int{},
		shardCnt:  map[uint8]int{},
	}
}

type snapshot struct{ total, ok, err int }

func (s *stats) add(l time.Duration, status int, err error, shard uint8) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latencies = append(s.latencies, l)
	s.statusCnt[status]++
	s.shardCnt[shard]++
	if err != nil {
		s.errCnt++
	} else {
		s.okCnt++
	}
}

func (s *stats) snapshot() snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return snapshot{total: len(s.latencies), ok: s.okCnt, err: s.errCnt}
}

func (s *stats) report(elapsed time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.latencies)
	if n == 0 {
		fmt.Println("no requests sent")
		return
	}
	sort.Slice(s.latencies, func(i, j int) bool { return s.latencies[i] < s.latencies[j] })
	p := func(q float64) time.Duration {
		idx := int(float64(n-1) * q)
		return s.latencies[idx]
	}
	rps := float64(n) / elapsed.Seconds()

	statuses := make([]int, 0, len(s.statusCnt))
	for k := range s.statusCnt {
		statuses = append(statuses, k)
	}
	sort.Ints(statuses)

	shards := make([]uint8, 0, len(s.shardCnt))
	for k := range s.shardCnt {
		shards = append(shards, k)
	}
	sort.Slice(shards, func(i, j int) bool { return shards[i] < shards[j] })

	fmt.Println()
	fmt.Println("==== bench results ====")
	fmt.Printf("duration:      %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("requests:      %d\n", n)
	fmt.Printf("ok / err:      %d / %d\n", s.okCnt, s.errCnt)
	fmt.Printf("throughput:    %.1f req/s\n", rps)
	fmt.Println("status codes:")
	for _, st := range statuses {
		fmt.Printf("  %d:           %d\n", st, s.statusCnt[st])
	}
	fmt.Println("latency:")
	fmt.Printf("  min:         %s\n", s.latencies[0])
	fmt.Printf("  p50:         %s\n", p(0.50))
	fmt.Printf("  p90:         %s\n", p(0.90))
	fmt.Printf("  p95:         %s\n", p(0.95))
	fmt.Printf("  p99:         %s\n", p(0.99))
	fmt.Printf("  max:         %s\n", s.latencies[n-1])
	if len(shards) > 0 {
		fmt.Println("shard distribution:")
		for _, sh := range shards {
			pct := 100.0 * float64(s.shardCnt[sh]) / float64(n)
			fmt.Printf("  shard %3d:   %d (%.1f%%)\n", sh, s.shardCnt[sh], pct)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
