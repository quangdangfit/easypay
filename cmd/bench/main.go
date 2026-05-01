// Bench is a self-contained load generator for the easypay API. It seeds a
// test merchant directly into MySQL, then floods POST /api/payments (and
// optionally GET /api/payments/:id) with HMAC-signed requests. At the end it
// prints throughput, error counts, and latency percentiles.
//
// Example:
//
//	go run ./cmd/bench --url http://localhost:8080 --concurrency 100 --total 10000
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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
	"github.com/joho/godotenv"

	"github.com/quangdangfit/easypay/pkg/hmac"
)

type config struct {
	url         string
	dbDSN       string
	concurrency int
	total       int
	duration    time.Duration
	merchantID  string
	apiKey      string
	secretKey   string
	method      string
	pollStatus  bool
	timeout     time.Duration
}

func main() {
	_ = godotenv.Load()

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
	}
	flag.StringVar(&cfg.url, "url", cfg.url, "base URL of the easypay server")
	flag.StringVar(&cfg.dbDSN, "db-dsn", cfg.dbDSN, "MySQL DSN (defaults to DB_DSN env)")
	flag.IntVar(&cfg.concurrency, "concurrency", cfg.concurrency, "number of concurrent workers")
	flag.IntVar(&cfg.total, "total", cfg.total, "total requests (ignored if --duration > 0)")
	flag.DurationVar(&cfg.duration, "duration", cfg.duration, "run for this long instead of --total")
	flag.StringVar(&cfg.merchantID, "merchant-id", cfg.merchantID, "merchant id to seed")
	flag.StringVar(&cfg.apiKey, "api-key", cfg.apiKey, "merchant api key")
	flag.StringVar(&cfg.secretKey, "secret-key", cfg.secretKey, "merchant secret key")
	flag.StringVar(&cfg.method, "method", cfg.method, "payment method: card or crypto")
	flag.BoolVar(&cfg.pollStatus, "poll-status", cfg.pollStatus, "after each create, GET /api/payments/:id once")
	flag.DurationVar(&cfg.timeout, "timeout", cfg.timeout, "per-request HTTP timeout")
	flag.Parse()
	return cfg
}

func run(cfg config) error {
	if cfg.dbDSN == "" {
		return fmt.Errorf("DB_DSN not set; pass --db-dsn or set env")
	}
	if err := seedMerchant(cfg); err != nil {
		return fmt.Errorf("seed merchant: %w", err)
	}

	httpClient := &http.Client{
		Timeout: cfg.timeout,
		Transport: &http.Transport{
			MaxIdleConns:        cfg.concurrency * 2,
			MaxIdleConnsPerHost: cfg.concurrency * 2,
			IdleConnTimeout:     30 * time.Second,
		},
	}

	type result struct {
		status  int
		latency time.Duration
		err     error
		orderID string
	}

	results := make(chan result, cfg.concurrency*2)
	var stop atomic.Bool
	var ctx context.Context
	var cancel context.CancelFunc
	if cfg.duration > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), cfg.duration)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
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
			if stop.Load() || ctx.Err() != nil {
				return
			}
			sent.Add(1)
			start := time.Now()
			res := result{}
			orderID, status, err := doCreate(ctx, httpClient, cfg, rng)
			res.status = status
			res.latency = time.Since(start)
			res.err = err
			res.orderID = orderID
			results <- res

			if cfg.pollStatus && err == nil && orderID != "" {
				start := time.Now()
				st, err := doGet(ctx, httpClient, cfg, orderID)
				results <- result{status: st, latency: time.Since(start), err: err}
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
			stats.add(r.latency, r.status, r.err)
		case <-progressTick.C:
			cur := stats.snapshot()
			fmt.Fprintf(os.Stderr, "[%s]  total=%d  ok=%d  err=%d  inst=%.0f rps\n",
				time.Since(t0).Round(time.Second),
				cur.total, cur.ok, cur.err,
				float64(cur.total-last.total)/progressEvery.Seconds(),
			)
			last = cur
		case <-ctx.Done():
			stop.Store(true)
		}
	}

	stats.report(time.Since(t0))
	return nil
}

func doCreate(ctx context.Context, c *http.Client, cfg config, rng *rand.Rand) (orderID string, status int, err error) {
	body := map[string]any{
		"merchant_order_id":    fmt.Sprintf("BENCH-%d-%s", time.Now().UnixNano(), uuid.NewString()[:8]),
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
	sig := hmac.Sign(cfg.secretKey, signed)

	url := cfg.url + "/api/payments"
	if cfg.method == "crypto" {
		url += "?method=crypto"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", cfg.apiKey)
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

func doGet(ctx context.Context, c *http.Client, cfg config, orderID string) (int, error) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := hmac.Sign(cfg.secretKey, []byte(ts+"."))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.url+"/api/payments/"+orderID, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-API-Key", cfg.apiKey)
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Signature", sig)
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("status %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func seedMerchant(cfg config) error {
	db, err := sql.Open("mysql", cfg.dbDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const q = `INSERT INTO merchants (merchant_id, name, api_key, secret_key, rate_limit, status)
	           VALUES (?, ?, ?, ?, ?, 'active')
	           ON DUPLICATE KEY UPDATE
	             secret_key = VALUES(secret_key),
	             rate_limit = VALUES(rate_limit),
	             status = 'active'`
	_, err = db.ExecContext(ctx, q, cfg.merchantID, "Bench Merchant", cfg.apiKey, cfg.secretKey, 1000000)
	return err
}

// stats accumulates results for end-of-run reporting.
type stats struct {
	mu        sync.Mutex
	latencies []time.Duration
	statusCnt map[int]int
	errCnt    int
	okCnt     int
}

func newStats() *stats {
	return &stats{statusCnt: map[int]int{}}
}

type snapshot struct{ total, ok, err int }

func (s *stats) add(l time.Duration, status int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latencies = append(s.latencies, l)
	s.statusCnt[status]++
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
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
