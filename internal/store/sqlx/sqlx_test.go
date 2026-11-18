package sqlx

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestBind(t *testing.T) {
	query := `SELECT a FROM t WHERE b = ? AND c IN (?, ?)`
	if got := SQLite.Bind(query); got != query {
		t.Fatalf("SQLite.Bind = %q", got)
	}
	if got := PostgreSQL.Bind(query); got != `SELECT a FROM t WHERE b = $1 AND c IN ($2, $3)` {
		t.Fatalf("PostgreSQL.Bind = %q", got)
	}
}

type pgError struct{ state string }

func (e pgError) Error() string    { return "pg " + e.state }
func (e pgError) SQLState() string { return e.state }

type sqliteError struct{ code int }

func (e sqliteError) Error() string { return fmt.Sprint("sqlite ", e.code) }
func (e sqliteError) Code() int     { return e.code }

func TestIsUniqueViolation(t *testing.T) {
	for err, want := range map[error]bool{
		pgError{"23505"}: true,
		pgError{"23503"}: false,
		fmt.Errorf("wrapped: %w", pgError{"23505"}): true,
		sqliteError{2067}:                      true,
		sqliteError{1555}:                      true,
		sqliteError{787}:                       false,
		errors.New("UNIQUE constraint failed"): false, // text is never trusted
	} {
		if got := IsUniqueViolation(err); got != want {
			t.Errorf("IsUniqueViolation(%v) = %v, want %v", err, got, want)
		}
	}
}

func TestTimeRoundTrip(t *testing.T) {
	now := time.Date(2026, 11, 18, 1, 2, 3, 456, time.FixedZone("x", 3600))
	parsed, err := ParseTime(Time(now))
	if err != nil || !parsed.Equal(now) || parsed.Location() != time.UTC {
		t.Fatalf("round trip = %v, %v", parsed, err)
	}
}
