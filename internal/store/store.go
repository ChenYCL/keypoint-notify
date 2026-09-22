// Package store owns the SQLite persistence layer. It is the only package that
// knows SQL; everything above it speaks model types.
package store

import (
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: keeps the binary CGO-free
)

//go:embed schema.sql
var schemaSQL string

// Store wraps the database handle.
type Store struct {
	db   *sql.DB
	root string // data directory; blobs live under root/blobs
}

// ErrNotFound is returned when a lookup by id/code/key matches nothing.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a unique constraint would be violated.
var ErrConflict = errors.New("conflict")

// Open opens (creating if needed) the database at dir/keypoint.db and runs the
// schema. dir also hosts uploaded blobs.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dsn := "file:" + filepath.Join(dir, "keypoint.db") +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(10000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// WAL allows concurrent readers; a handful of connections is plenty for a
	// hub of this size and keeps writer contention handled by busy_timeout.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db, root: dir}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Dir returns the data directory (blob storage root).
func (s *Store) Dir() string { return s.root }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Transaction helpers
// ---------------------------------------------------------------------------

// tx runs fn inside a transaction, rolling back on error and committing on nil.
func (s *Store) tx(fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// querier is satisfied by both *sql.DB and *sql.Tx.
type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// ---------------------------------------------------------------------------
// Small shared helpers
// ---------------------------------------------------------------------------

// NewID returns a short, sortable-enough random identifier. 10 bytes of
// entropy base32-encoded gives 16 chars — long enough to never collide in
// practice, short enough to paste into a chat.
func NewID(prefix string) string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
	return prefix + lowerASCII(enc)
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// nextCounter atomically increments a named counter inside tx and returns the
// new value. Counters start at zero so the first caller gets 1 — which is what
// makes the first task KP-1 rather than KP-2.
func nextCounter(tx *sql.Tx, name string) (int, error) {
	if _, err := tx.Exec(
		`INSERT INTO counters(name, next) VALUES(?, 0)
		 ON CONFLICT(name) DO NOTHING`, name); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE counters SET next = next + 1 WHERE name = ?`, name); err != nil {
		return 0, err
	}
	var v int
	if err := tx.QueryRow(`SELECT next FROM counters WHERE name = ?`, name).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// nowMS is the single source of "current time" written to the database.
func nowMS() int64 { return time.Now().UTC().UnixMilli() }

func msToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func nullableMS(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().UnixMilli()
}

func msToTimePtr(ms sql.NullInt64) *time.Time {
	if !ms.Valid || ms.Int64 == 0 {
		return nil
	}
	t := time.UnixMilli(ms.Int64).UTC()
	return &t
}

// jsonStrings marshals a string slice, tolerating nil (stores "[]").
func jsonStrings(v []string) string {
	if v == nil {
		return "[]"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// scanStrings unmarshals a JSON array column, tolerating empty/garbage.
func scanStrings(raw string) []string {
	if raw == "" || raw == "null" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []string{}
	}
	if out == nil {
		return []string{}
	}
	return out
}

// jsonAny marshals an arbitrary value, falling back to "{}".
func jsonAny(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func scanAny(raw string) map[string]any {
	out := map[string]any{}
	if raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// isUniqueViolation reports whether err is a SQLite UNIQUE/PRIMARY KEY clash.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE") ||
		strings.Contains(msg, "PRIMARY KEY")
}

// ---------------------------------------------------------------------------
// Meta (small key/value store for cursors and one-off markers)
// ---------------------------------------------------------------------------

// GetMeta reads a value, returning "" when unset.
func (s *Store) GetMeta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetMeta writes a value, creating the row if needed.
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES(?,?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
