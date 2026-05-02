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
