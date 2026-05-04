package repository

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/quangdangfit/easypay/internal/config"
)

func TestWithClientFoundRows(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"u:p@tcp(x)/y", "u:p@tcp(x)/y?clientFoundRows=true"},
		{"u:p@tcp(x)/y?parseTime=true", "u:p@tcp(x)/y?parseTime=true&clientFoundRows=true"},
		{"u:p@tcp(x)/y?clientFoundRows=true", "u:p@tcp(x)/y?clientFoundRows=true"},
	}
	for _, c := range cases {
		got := withClientFoundRows(c.in)
		if got != c.want {
			t.Errorf("\nin:   %s\ngot:  %s\nwant: %s", c.in, got, c.want)
		}
	}
}

func TestOpenMySQL_PingFailure(t *testing.T) {
	t.Parallel()
	_, err := OpenMySQL(config.DBConfig{
		DSN:          "root:root@tcp(127.0.0.1:1)/payments?timeout=100ms",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	if err == nil {
		t.Fatal("expected ping mysql error")
	}
	if !strings.Contains(err.Error(), "ping mysql") {
		t.Fatalf("error %q does not contain ping mysql", err)
	}
}

// MySQLPinger wraps *sql.DB; we test via sqlmock so the ping just exercises
// the driver call path.
func TestMySQLPinger(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectPing()

	p := &MySQLPinger{DB: db}
	if err := p.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestSplitSQLStatements_StripsLineCommentsAndSemicolons(t *testing.T) {
	// A ';' inside a '--' comment must not split the statement that follows.
	body := `-- header comment; with a semicolon inside
--   continuation; and another;
CREATE TABLE a (
    id INT
);

-- divider
CREATE TABLE b (id INT);
`
	got := splitSQLStatements(body)
	if len(got) != 2 {
		t.Fatalf("want 2 statements, got %d: %#v", len(got), got)
	}
	if !strings.HasPrefix(got[0], "CREATE TABLE a") {
		t.Errorf("stmt[0] = %q, want CREATE TABLE a ...", got[0])
	}
	if !strings.HasPrefix(got[1], "CREATE TABLE b") {
		t.Errorf("stmt[1] = %q, want CREATE TABLE b ...", got[1])
	}
}

func TestRunMigrationsAll_AppliesSQLFile(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectExec("CREATE TABLE a \\(id INT\\)").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CREATE TABLE b \\(id INT\\)").WillReturnResult(sqlmock.NewResult(0, 0))

	dir := t.TempDir()
	body := "-- comment with ; semicolon\nCREATE TABLE a (id INT);\nCREATE TABLE b (id INT);\n"
	path := filepath.Join(dir, "001_init.up.sql")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	r := NewSingleShardRouter(db, 1)
	if err := RunMigrationsAll(r, dir); err != nil {
		t.Fatalf("RunMigrationsAll: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestRunMigrationsAll_ExecError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectExec("CREATE TABLE fail_me \\(id INT\\)").WillReturnError(errors.New("boom"))

	dir := t.TempDir()
	path := filepath.Join(dir, "001_fail.up.sql")
	if err := os.WriteFile(path, []byte("CREATE TABLE fail_me (id INT);"), 0o600); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	r := NewSingleShardRouter(db, 1)
	err = RunMigrationsAll(r, dir)
	if err == nil {
		t.Fatal("expected migration apply error")
	}
	if !strings.Contains(err.Error(), "apply 001_fail.up.sql on pool 0") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// scanTransaction error path — pass a row that's been closed to force a scan error.
func TestScanTransaction_ScanError(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer func() { _ = db.Close() }()
	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"id"}).AddRow(int64(1)), // wrong column count
	)
	row := db.QueryRow("SELECT id FROM x")
	_, err := scanTransaction(row)
	if err == nil {
		t.Fatal("expected scan error")
	}
	// Either wrap message ("scan transaction") or driver-level ("Scan").
	if !strings.Contains(err.Error(), "scan") && !strings.Contains(err.Error(), "Scan") {
		t.Logf("unusual error string: %v", err)
	}
	_ = sql.ErrNoRows
}
