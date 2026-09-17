// Package store keeps computed analyses in DuckDB so the same question does
// not pay for the same work twice.
//
// An analysis costs a data fetch plus a few seconds of simulation, and the
// answer only changes when the market day changes. Keying on the market day
// and the model's parameters therefore makes a repeat question instant,
// while a new trading day, a different path count or a changed model all
// miss the cache and recompute.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// Store is a DuckDB-backed cache of analyses and a log of what was asked.
type Store struct {
	db *sql.DB
}

// Key identifies one analysis. Everything that can change the answer is part
// of it, so a stale result can never be served as a fresh one.
type Key struct {
	Symbol       string
	AsOf         time.Time // the market day the analysis is for
	Paths        int
	Seed         uint64
	ModelVersion string
}

const schema = `
CREATE TABLE IF NOT EXISTS analyses (
	symbol        TEXT      NOT NULL,
	as_of         DATE      NOT NULL,
	paths         INTEGER   NOT NULL,
	seed          BIGINT    NOT NULL,
	model_version TEXT      NOT NULL,
	computed_at   TIMESTAMP NOT NULL,
	compute_ms    BIGINT    NOT NULL,
	payload       JSON      NOT NULL,
	PRIMARY KEY (symbol, as_of, paths, seed, model_version)
);
CREATE TABLE IF NOT EXISTS requests (
	requested_at TIMESTAMP NOT NULL,
	symbol       TEXT      NOT NULL,
	cached       BOOLEAN   NOT NULL
);
`

// ErrLocked is returned when something else already holds the database.
// DuckDB allows a single writer and no readers beside it, so a running
// server owns the file until it exits.
var ErrLocked = errors.New("store: the database is already in use")

// Open opens or creates the database at path and applies the schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, openError(path, err)
	}
	// DuckDB takes a single writer, so one connection avoids lock contention
	// between concurrent analyses.
	db.SetMaxOpenConns(1)
	if err := dropLegacyIdentifiers(db); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		if isLockError(err) {
			return nil, openError(path, err)
		}
		return nil, fmt.Errorf("store: schema: %w", err)
	}
	return &Store{db: db}, nil
}

// dropLegacyIdentifiers removes the request log written by versions that
// recorded an email address with each question. The table held nothing but
// that log, so dropping it erases the addresses rather than leaving them in
// a file nobody looks at.
func dropLegacyIdentifiers(db *sql.DB) error {
	var found int
	err := db.QueryRow(`
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'requests' AND column_name = 'email'`).Scan(&found)
	if err != nil {
		return fmt.Errorf("store: inspect requests: %w", err)
	}
	if found == 0 {
		return nil
	}
	if _, err := db.Exec(`DROP TABLE requests`); err != nil {
		return fmt.Errorf("store: drop legacy requests: %w", err)
	}
	return nil
}

// OpenReadOnly opens an existing database for inspection. It still needs
// the file to be free of a writer, but it will never create or modify one.
func OpenReadOnly(path string) (*Store, error) {
	db, err := sql.Open("duckdb", path+"?access_mode=read_only")
	if err != nil {
		return nil, openError(path, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, openError(path, err)
	}
	return &Store{db: db}, nil
}

// openError classifies a failure to open the database, so a caller can tell
// "something else has it" from anything else. The driver connects eagerly,
// so the lock shows up at sql.Open as readily as at the first query.
func openError(path string, err error) error {
	if isLockError(err) {
		return fmt.Errorf("%w: %s", ErrLocked, path)
	}
	return fmt.Errorf("store: open %s: %w", path, err)
}

// isLockError recognises the two ways DuckDB says the file is spoken for,
// neither of them a typed error: another process holds the lock, or this
// process already has it open under a different configuration.
func isLockError(err error) bool {
	text := err.Error()
	for _, marker := range []string{
		"Could not set lock",
		"Conflicting lock",
		"different configuration than existing connections",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// Close releases the database. A nil Store closes cleanly, so callers can
// treat caching as optional without branching.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	return s.db.Close()
}

// Get returns the stored analysis for key, if one is present.
func (s *Store) Get(ctx context.Context, key Key) (json.RawMessage, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	var body string
	err := s.db.QueryRowContext(ctx, `
		SELECT payload::TEXT FROM analyses
		WHERE symbol = ? AND as_of = ? AND paths = ? AND seed = ? AND model_version = ?`,
		key.Symbol, key.AsOf.Format(time.DateOnly), key.Paths, int64(key.Seed), key.ModelVersion,
	).Scan(&body)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: get %s: %w", key.Symbol, err)
	}
	return json.RawMessage(body), true, nil
}

// Put stores an analysis, replacing any earlier result for the same key.
func (s *Store) Put(ctx context.Context, key Key, view json.RawMessage, compute time.Duration) error {
	if s == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO analyses
			(symbol, as_of, paths, seed, model_version, computed_at, compute_ms, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		key.Symbol, key.AsOf.Format(time.DateOnly), key.Paths, int64(key.Seed), key.ModelVersion,
		time.Now().UTC(), compute.Milliseconds(), string(view),
	)
	if err != nil {
		return fmt.Errorf("store: put %s: %w", key.Symbol, err)
	}
	return nil
}

// RecordRequest counts a question and whether the cache answered it. It
// records no identifier of any kind, so the table says how the machine is
// used without saying by whom.
func (s *Store) RecordRequest(ctx context.Context, symbol string, cached bool) error {
	if s == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO requests (requested_at, symbol, cached) VALUES (?, ?, ?)`,
		time.Now().UTC(), symbol, cached)
	if err != nil {
		return fmt.Errorf("store: record request: %w", err)
	}
	return nil
}

// Stats summarises what the cache holds.
type Stats struct {
	Analyses      int
	Symbols       int
	Requests      int
	CacheHits     int
	OldestAsOf    string
	NewestAsOf    string
	MeanComputeMS float64
}

// Stats reports the contents of the database, for the cache subcommand.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	if s == nil {
		return st, nil
	}
	var oldest, newest sql.NullString
	var mean sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*), count(DISTINCT symbol), min(as_of)::TEXT, max(as_of)::TEXT, avg(compute_ms)
		FROM analyses`).Scan(&st.Analyses, &st.Symbols, &oldest, &newest, &mean)
	if err != nil {
		return st, fmt.Errorf("store: stats: %w", err)
	}
	st.OldestAsOf, st.NewestAsOf, st.MeanComputeMS = oldest.String, newest.String, mean.Float64

	// The requests table may be empty, which is not an error.
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*), coalesce(sum(CASE WHEN cached THEN 1 ELSE 0 END), 0) FROM requests`).
		Scan(&st.Requests, &st.CacheHits); err != nil {
		return st, fmt.Errorf("store: request stats: %w", err)
	}
	return st, nil
}

// Prune deletes analyses for market days older than the cutoff and returns
// how many rows went.
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	if s == nil {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM analyses WHERE as_of < ?`, before.Format(time.DateOnly))
	if err != nil {
		return 0, fmt.Errorf("store: prune: %w", err)
	}
	return res.RowsAffected()
}
