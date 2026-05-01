package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/quangdangfit/easypay/internal/config"
)

func OpenMySQL(cfg config.DBConfig) (*sql.DB, error) {
	dsn := withClientFoundRows(cfg.DSN)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	return db, nil
}

// withClientFoundRows ensures the MySQL DSN includes `clientFoundRows=true`
// so that UPDATE statements report matched rows rather than only changed
// rows. This avoids RowsAffected==0 false positives when an UPDATE writes
// the same value the row already holds (idempotent webhook replays, etc.).
func withClientFoundRows(dsn string) string {
	if dsn == "" || strings.Contains(dsn, "clientFoundRows=") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "clientFoundRows=true"
}

// MySQLPinger adapts *sql.DB to handler.Pinger.
type MySQLPinger struct{ DB *sql.DB }

func (p *MySQLPinger) Ping(ctx context.Context) error { return p.DB.PingContext(ctx) }

// OpenShardPools returns a slice of *sql.DB pools sized to ShardCount.
// `dsns` is either:
//   - a single string (all shards collocated on one MySQL instance — dev /
//     small deployments). Returns ShardCount references to the same pool.
//   - exactly ShardCount strings (one pool per shard).
//
// Per-shard pools share the same connection-pool tunables; size them down
// at the call site if you have many shards × many pods.
func OpenShardPools(dsns []string, base config.DBConfig) ([]*sql.DB, error) {
	switch len(dsns) {
	case 0:
		// Fall back to single-DSN config.
		db, err := OpenMySQL(base)
		if err != nil {
			return nil, err
		}
		out := make([]*sql.DB, ShardCount)
		for i := range out {
			out[i] = db
		}
		return out, nil
	case 1:
		base.DSN = dsns[0]
		db, err := OpenMySQL(base)
		if err != nil {
			return nil, err
		}
		out := make([]*sql.DB, ShardCount)
		for i := range out {
			out[i] = db
		}
		return out, nil
	case ShardCount:
		out := make([]*sql.DB, 0, ShardCount)
		for i, dsn := range dsns {
			cfg := base
			cfg.DSN = dsn
			db, err := OpenMySQL(cfg)
			if err != nil {
				// Close already-opened pools before returning.
				for _, p := range out {
					_ = p.Close()
				}
				return nil, fmt.Errorf("open shard %d: %w", i, err)
			}
			out = append(out, db)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("shard pool count: want 0/1/%d DSNs, got %d", ShardCount, len(dsns))
	}
}
