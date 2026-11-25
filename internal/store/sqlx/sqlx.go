// Package sqlx holds the small helpers every SQL adapter shares: the
// dialect and its placeholder syntax, transaction and row cleanup, affected-row
// checks, unique-violation detection and the stored timestamp format. It
// imports no driver, so it serves SQLite and PostgreSQL alike (AUDIT X-01 to
// X-03).
package sqlx

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Dialect is the SQL engine a query is written for.
type Dialect uint8

// Supported dialects.
const (
	SQLite Dialect = iota + 1
	PostgreSQL
)

// Valid reports whether d is a supported dialect.
func (d Dialect) Valid() bool { return d == SQLite || d == PostgreSQL }

// Bind rewrites ? placeholders to $1, $2, ... for PostgreSQL. Queries are
// written once with ? and must not contain a literal question mark.
func (d Dialect) Bind(query string) string {
	if d != PostgreSQL {
		return query
	}
	var out strings.Builder
	index := 1
	for _, r := range query {
		if r == '?' {
			fmt.Fprintf(&out, "$%d", index)
			index++
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// Bool encodes a boolean for the dialect (SQLite stores 0/1).
func (d Dialect) Bool(value bool) any {
	if d == PostgreSQL {
		return value
	}
	if value {
		return 1
	}
	return 0
}

// Rollback rolls back tx, ignoring sql.ErrTxDone after a commit; use it in a
// defer right after BeginTx.
func Rollback(tx *sql.Tx, logger *slog.Logger, operation string) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		logger.Warn("rollback transaction", "operation", operation, "error", err)
	}
}

// CloseRows closes rows and logs a failure.
func CloseRows(rows *sql.Rows, logger *slog.Logger, operation string) {
	if err := rows.Close(); err != nil {
		logger.Warn("close database rows", "operation", operation, "error", err)
	}
}

// RowsAffected returns notFound when result changed no row.
func RowsAffected(result sql.Result, notFound error) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected row count: %w", err)
	}
	if count == 0 {
		return notFound
	}
	return nil
}

// SQLite extended result codes for constraint violations.
const (
	sqliteConstraintPrimaryKey = 1555
	sqliteConstraintUnique     = 2067
)

// IsUniqueViolation reports whether err is a unique or primary-key violation
// from either driver: PostgreSQL SQLSTATE 23505, or SQLite's extended codes.
func IsUniqueViolation(err error) bool {
	var postgres interface{ SQLState() string }
	if errors.As(err, &postgres) {
		return postgres.SQLState() == "23505"
	}
	var sqlite interface{ Code() int }
	if errors.As(err, &sqlite) {
		code := sqlite.Code()
		return code == sqliteConstraintUnique || code == sqliteConstraintPrimaryKey
	}
	return false
}

// timeLayout is RFC 3339 in UTC with exactly nine fractional digits.
// time.RFC3339Nano drops trailing zeros ("…:00Z" for a whole second, "…:00.5Z"
// for half a second), and as text "Z" sorts after ".", so a whole second
// compared as later than the half second after it. Fixed width makes text
// order equal time order, which every SQL comparison of a stored time relies
// on (AUDIT F14-01).
const timeLayout = "2006-01-02T15:04:05.000000000Z"

// Time formats a timestamp the way every table stores it: fixed-width RFC 3339
// text in UTC with nanoseconds, identical in both dialects, ordered like the
// times it stands for. ParseTime also reads the older variable-width form.
func Time(value time.Time) string { return value.UTC().Format(timeLayout) }

// ParseTime reads a timestamp written by Time.
func ParseTime(text string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse stored timestamp %q: %w", text, err)
	}
	return parsed.UTC(), nil
}

// ParseNullTime reads an optional timestamp.
func ParseNullTime(text sql.NullString) (*time.Time, error) {
	if !text.Valid || text.String == "" {
		return nil, nil
	}
	parsed, err := ParseTime(text.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
