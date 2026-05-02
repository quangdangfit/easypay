//go:build integration

package integration

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/repository"
)

// TestBlockchainTxHashDedup confirms the UNIQUE(tx_hash) constraint on
// pending_txs blocks duplicate inserts. This is the database-level safety net
// for the chain subscriber + backfill scanner double-emit case.
func TestBlockchainTxHashDedup(t *testing.T) {
	env := SetupEnv(t)
	defer env.Cleanup(t)

	repo := repository.NewOnchainTxRepository(env.DB)

	hash := "0x" + strings.Repeat("ab", 32) // 66-char hex
	tx := &domain.OnchainTransaction{
		TxHash:          hash,
		BlockNumber:     1234,
		OrderID:         "ord-chain-1",
		Payer:           "0x" + strings.Repeat("11", 20),
		Token:           "0x" + strings.Repeat("22", 20),
		Amount:          big.NewInt(1_000_000_000),
		ChainID:         11155111,
		Confirmations:   0,
		RequiredConfirm: 12,
		Status:          domain.OnchainTxStatusPending,
	}
	if err := repo.Create(context.Background(), tx); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Same tx_hash, different other fields — should be rejected.
	dup := &domain.OnchainTransaction{
		TxHash:          hash,
		BlockNumber:     5678,
		OrderID:         "ord-chain-2",
		Amount:          big.NewInt(1),
		ChainID:         11155111,
		RequiredConfirm: 12,
		Status:          domain.OnchainTxStatusPending,
	}
	err := repo.Create(context.Background(), dup)
	if err == nil {
		t.Fatal("expected duplicate insert to fail")
	}

	// Make sure the error is the MySQL ER_DUP_ENTRY (1062), not some other
	// failure mode (connection drop, schema mismatch).
	var mysqlErr *gomysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		t.Fatalf("expected *mysql.MySQLError, got %T: %v", err, err)
	}
	if mysqlErr.Number != 1062 {
		t.Fatalf("expected MySQL error 1062 (duplicate entry), got %d: %s",
			mysqlErr.Number, mysqlErr.Message)
	}

	// And the original row remains intact.
	got, err := repo.GetByTxHash(context.Background(), hash)
	if err != nil {
		t.Fatalf("get original: %v", err)
	}
	if got.OrderID != "ord-chain-1" || got.BlockNumber != 1234 {
		t.Fatalf("original mutated: %+v", got)
	}
}
