//go:build integration

package integration

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go/modules/kafka"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/internal/service"
)

// TestEnv holds connections to dockerised dependencies for integration tests.
// Use SetupEnv to spin everything up; defer Cleanup to tear it down.
//
// Router wraps DB as a single-pool ShardRouter — sufficient for the
// integration suite, which uses one MySQL container. Production runs N
// pools; sharding is exercised by unit tests against the router itself.
type TestEnv struct {
	DB           *sql.DB
	Router       repository.ShardRouter
	Redis        *redis.Client
	KafkaBrokers []string

	mysqlC *tcmysql.MySQLContainer
	redisC *tcredis.RedisContainer
	kafkaC *kafka.KafkaContainer
}

func SetupEnv(t *testing.T) *TestEnv {
	t.Helper()
	if os.Getenv("EASYPAY_INTEGRATION") == "" {
		t.Skip("integration tests disabled — set EASYPAY_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	env := &TestEnv{}

	mysqlC, err := tcmysql.Run(ctx, "mysql:8.0",
		tcmysql.WithDatabase("payments"),
		tcmysql.WithUsername("root"),
		tcmysql.WithPassword("password"),
	)
	if err != nil {
		t.Fatalf("mysql start: %v", err)
	}
	env.mysqlC = mysqlC

	dsn, err := mysqlC.ConnectionString(ctx, "parseTime=true&multiStatements=true&clientFoundRows=true")
	if err != nil {
		t.Fatalf("mysql conn: %v", err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("mysql open: %v", err)
	}
	env.DB = db
	// LogicalShardCount of 16 matches the production default. The router
	// is single-pool: every logical index routes to the same DB.
	env.Router = repository.NewSingleShardRouter(db, 16)

	if err := runMigrations(db); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	redisC, err := tcredis.Run(ctx, "redis:7.4-alpine")
	if err != nil {
		t.Fatalf("redis start: %v", err)
	}
	env.redisC = redisC
	redisURL, err := redisC.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis conn: %v", err)
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	env.Redis = redis.NewClient(opts)

	kafkaC, err := kafka.Run(ctx, "confluentinc/confluent-local:7.6.0",
		kafka.WithClusterID("test-cluster"),
	)
	if err != nil {
		t.Fatalf("kafka start: %v", err)
	}
	env.kafkaC = kafkaC
	brokers, err := kafkaC.Brokers(ctx)
	if err != nil {
		t.Fatalf("kafka brokers: %v", err)
	}
	env.KafkaBrokers = brokers

	return env
}

func (e *TestEnv) Cleanup(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if e.DB != nil {
		_ = e.DB.Close()
	}
	if e.Redis != nil {
		_ = e.Redis.Close()
	}
	if e.kafkaC != nil {
		_ = e.kafkaC.Terminate(ctx)
	}
	if e.redisC != nil {
		_ = e.redisC.Terminate(ctx)
	}
	if e.mysqlC != nil {
		_ = e.mysqlC.Terminate(ctx)
	}
}

func runMigrations(db *sql.DB) error {
	root := projectRoot()
	files, err := filepath.Glob(filepath.Join(root, "migrations", "*.up.sql"))
	if err != nil {
		return err
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		for _, stmt := range splitSQL(string(b)) {
			if _, err := db.Exec(stmt); err != nil {
				return fmt.Errorf("apply %s: %w", filepath.Base(f), err)
			}
		}
	}
	return nil
}

// splitSQL strips '--' line comments and splits the remainder on ';'.
// Mirrors repository.splitSQLStatements — a bare split on ';' breaks the
// moment a comment line contains a semicolon (our schema docstrings do).
func splitSQL(body string) []string {
	var sanitized strings.Builder
	sanitized.Grow(len(body))
	for _, line := range strings.Split(body, "\n") {
		if trimmed := strings.TrimLeft(line, " \t"); strings.HasPrefix(trimmed, "--") {
			continue
		}
		sanitized.WriteString(line)
		sanitized.WriteByte('\n')
	}
	parts := strings.Split(sanitized.String(), ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// DeriveTransactionID matches payment_service.DeriveTransactionID — tests
// that seed orders with the same (merchant_id, order_id) the service would
// see can use this to compute the matching transaction_id.
func DeriveTransactionID(merchantID, orderID string) string {
	return service.DeriveTransactionID(merchantID, orderID)
}

// SeedOrder inserts a row with deterministic transaction_id derived from
// (merchant_id, order_id). The order_id is merchant-supplied, so callers
// pass it verbatim.
func SeedOrder(t *testing.T, repo repository.TransactionRepository, merchantID, orderID string, amount int64, status domain.TransactionStatus, opts ...func(*domain.Transaction)) *domain.Transaction {
	t.Helper()
	o := &domain.Transaction{
		MerchantID:    merchantID,
		TransactionID: DeriveTransactionID(merchantID, orderID),
		OrderID:       orderID,
		Amount:        amount,
		Currency:      "USD",
		Status:        status,
		PaymentMethod: "card",
	}
	for _, f := range opts {
		f(o)
	}
	if err := repo.Insert(context.Background(), o); err != nil {
		t.Fatalf("seed order %s: %v", orderID, err)
	}
	return o
}

// SeedMerchant inserts (or upserts) a merchant row so callback lookups
// resolve. Most integration tests don't need this, but the e2e flow does.
// shard_index defaults to 0 — sufficient since today's transactions table
// is unsharded physically.
func SeedMerchant(t *testing.T, db *sql.DB, merchantID, secretKey, callbackURL string) {
	t.Helper()
	const q = `INSERT INTO merchants (merchant_id, name, api_key, secret_key, callback_url, rate_limit, status, shard_index)
	           VALUES (?, ?, ?, ?, ?, 1000, 'active', 0)
	           ON DUPLICATE KEY UPDATE
	             secret_key = VALUES(secret_key),
	             callback_url = VALUES(callback_url),
	             status = 'active'`
	apiKey := merchantID + "-api"
	if _, err := db.ExecContext(context.Background(), q, merchantID, merchantID, apiKey, secretKey, callbackURL); err != nil {
		t.Fatalf("seed merchant %s: %v", merchantID, err)
	}
}

// BackdateOrder rewrites an order's created_at so the reconciliation cron's
// "stuck" predicate fires without a real wait. Used by reconciliation_test.go.
func BackdateOrder(t *testing.T, db *sql.DB, merchantID, orderID string, age time.Duration) {
	t.Helper()
	created := time.Now().Add(-age).Unix()
	if created < 0 {
		created = 0
	}
	q := "UPDATE transactions SET created_at = ? WHERE merchant_id = ? AND order_id = ?"
	if _, err := db.ExecContext(context.Background(), q, uint32(created), []byte(merchantID), orderID); err != nil {
		t.Fatalf("backdate order: %v", err)
	}
}

func projectRoot() string {
	wd, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return wd
		}
		wd = parent
	}
}
