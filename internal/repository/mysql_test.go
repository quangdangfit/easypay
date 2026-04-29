package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
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

// scanOrder error path — pass a row that's been closed to force a scan error.
func TestScanOrder_ScanError(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer func() { _ = db.Close() }()
	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"id"}).AddRow(int64(1)), // wrong column count
	)
	row := db.QueryRow("SELECT id FROM x")
	_, err := scanOrder(row)
	if err == nil {
		t.Fatal("expected scan error")
	}
	// Either wrap message ("scan order") or driver-level ("Scan").
	if !strings.Contains(err.Error(), "scan") && !strings.Contains(err.Error(), "Scan") {
		t.Logf("unusual error string: %v", err)
	}
	_ = sql.ErrNoRows
}
