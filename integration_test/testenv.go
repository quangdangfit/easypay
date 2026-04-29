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
)

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
