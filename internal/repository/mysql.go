package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/quangdangfit/easypay/internal/config"
)

func OpenMySQL(cfg config.DBConfig) (*sql.DB, error) {
	return openPool(cfg.DSN, cfg.MaxOpenConns, cfg.MaxIdleConns)
}

// openPool opens a single *sql.DB pool, applies pool tuning, and verifies
// connectivity with a short ping. Used by both OpenMySQL (legacy single
// DSN) and OpenShards (multi-pool router).
func openPool(dsn string, maxOpen, maxIdle int) (*sql.DB, error) {
	dsn = withClientFoundRows(dsn)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	return db, nil
}

// RunMigrationsAll applies every migrations/*.up.sql file to every pool in
// the router. Tables only used by the control plane (merchants,
// onchain_transactions, block_cursors) are created on every shard but sit
// empty on non-control shards — keeps the migration runner branch-free.
func RunMigrationsAll(router ShardRouter, migrationsDir string) error {
	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.up.sql"))
	if err != nil {
		return fmt.Errorf("glob migrations: %w", err)
	}
	pools := router.All()
	for _, f := range files {
		// #nosec G304 -- migrationsDir is operator-supplied configuration.
		body, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", f, err)
		}
		stmts := splitSQLStatements(string(body))
		for poolIdx, db := range pools {
			for _, stmt := range stmts {
				if _, err := db.Exec(stmt); err != nil {
					return fmt.Errorf("apply %s on pool %d: %w", filepath.Base(f), poolIdx, err)
				}
			}
		}
	}
	return nil
}

func splitSQLStatements(body string) []string {
	// Strip '--' line comments before splitting on ';'. A bare split on ';'
	// breaks the moment a comment line contains a semicolon, which our
	// schema docstrings do.
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
		s := strings.TrimSpace(p)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
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

// MySQLPinger adapts *sql.DB to handler.Pinger. Multi-pool deployments
// should use ShardRouterPinger which pings every pool.
type MySQLPinger struct{ DB *sql.DB }

func (p *MySQLPinger) Ping(ctx context.Context) error { return p.DB.PingContext(ctx) }

// ShardRouterPinger adapts a ShardRouter to handler.Pinger by pinging every
// pool sequentially. Returns the first error.
type ShardRouterPinger struct{ Router ShardRouter }

func (p *ShardRouterPinger) Ping(ctx context.Context) error {
	for i, db := range p.Router.All() {
		if err := db.PingContext(ctx); err != nil {
			return fmt.Errorf("shard %d: %w", i, err)
		}
	}
	return nil
}
