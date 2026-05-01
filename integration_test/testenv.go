//go:build integration

package integration

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
)

// txnSecretForTests is the HMAC key the integration suite uses to derive
// transaction_id and order_id from a (merchant_id, slug) pair. Tests that
// stand up a payment service should reuse this value so the IDs stay
// stable across the suite.
const txnSecretForTests = "integration-test-transaction-id-secret"

// TestEnv holds connections to dockerised dependencies for integration tests.
// Use SetupEnv to spin everything up; defer Cleanup to tear it down.
type TestEnv struct {
	DB           *sql.DB
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
		for _, stmt := range strings.Split(string(b), ";") {
			s := strings.TrimSpace(stmt)
			if s == "" {
				continue
			}
			if _, err := db.Exec(s); err != nil {
				return fmt.Errorf("apply %s: %w", filepath.Base(f), err)
			}
		}
	}
	return nil
}

// DeriveIDs returns the (transaction_id, order_id) pair for a slug, matching
// payment_service.deriveIDs verbatim. Tests can use this to seed a row whose
// IDs will line up with what the service would derive for the same input.
func DeriveIDs(merchantID, slug string) (txnHex, orderHex string) {
	h := hmac.New(sha256.New, []byte(txnSecretForTests))
	h.Write([]byte(merchantID))
	h.Write([]byte{':'})
	h.Write([]byte(slug))
	sum := h.Sum(nil)

	txn := make([]byte, 16)
	copy(txn, sum[:16])

	ord := make([]byte, 12)
	ord[0] = repository.ShardOf(merchantID)
	copy(ord[1:], txn[1:12])

	return hex.EncodeToString(txn), hex.EncodeToString(ord)
}

// SeedOrder creates a row via the sharded repo using deterministic IDs.
// Returns the persisted row so callers don't have to re-derive the IDs.
func SeedOrder(t *testing.T, repo repository.OrderRepository, merchantID, slug string, amount int64, status domain.OrderStatus, opts ...func(*domain.Order)) *domain.Order {
	t.Helper()
	txn, ord := DeriveIDs(merchantID, slug)
	o := &domain.Order{
		MerchantID:    merchantID,
		TransactionID: txn,
		OrderID:       ord,
		Amount:        amount,
		Currency:      "USD",
		Status:        status,
		PaymentMethod: "card",
	}
	for _, f := range opts {
		f(o)
	}
	if err := repo.Insert(context.Background(), o); err != nil {
		t.Fatalf("seed order %s: %v", slug, err)
	}
	return o
}

// SeedMerchant inserts (or upserts) a merchant row so callback lookups
// resolve. Most integration tests don't need this, but the e2e flow does.
func SeedMerchant(t *testing.T, db *sql.DB, merchantID, secretKey, callbackURL string) {
	t.Helper()
	const q = `INSERT INTO merchants (merchant_id, name, api_key, secret_key, callback_url, rate_limit, status)
	           VALUES (?, ?, ?, ?, ?, 1000, 'active')
	           ON DUPLICATE KEY UPDATE
	             secret_key = VALUES(secret_key),
	             callback_url = VALUES(callback_url),
	             status = 'active'`
	apiKey := merchantID + "-api"
	if _, err := db.ExecContext(context.Background(), q, merchantID, merchantID, apiKey, secretKey, callbackURL); err != nil {
		t.Fatalf("seed merchant %s: %v", merchantID, err)
	}
}

// BackdateOrder rewrites an order's created_at on its shard so the
// reconciliation cron's "stuck" predicate fires without a real wait.
// Used by reconciliation_test.go.
func BackdateOrder(t *testing.T, db *sql.DB, merchantID, orderHex string, age time.Duration) {
	t.Helper()
	shard := repository.ShardOf(merchantID)
	ord, err := hex.DecodeString(orderHex)
	if err != nil {
		t.Fatalf("decode order_id: %v", err)
	}
	created := time.Now().Add(-age).Unix()
	if created < 0 {
		created = 0
	}
	q := "UPDATE " + repository.ShardTable(shard) + " SET created_at = ? WHERE order_id = ?"
	if _, err := db.ExecContext(context.Background(), q, uint32(created), ord); err != nil {
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
