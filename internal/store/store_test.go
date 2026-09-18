package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testKey(symbol string, day time.Time) Key {
	return Key{Symbol: symbol, AsOf: day, Paths: 20000, Seed: 1, ModelVersion: "v1"}
}

func TestPutThenGet(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	key := testKey("AAPL", day)

	if _, ok, err := s.Get(ctx, key, 0); err != nil || ok {
		t.Fatalf("empty store returned ok=%v err=%v", ok, err)
	}
	payload := json.RawMessage(`{"symbol":"AAPL","stance":"bullish","score":0.76}`)
	if err := s.Put(ctx, key, payload, 4*time.Second); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(ctx, key, 0)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	var view struct {
		Symbol string  `json:"symbol"`
		Score  float64 `json:"score"`
	}
	if err := json.Unmarshal(got, &view); err != nil {
		t.Fatal(err)
	}
	if view.Symbol != "AAPL" || view.Score != 0.76 {
		t.Fatalf("round trip lost data: %+v", view)
	}
}

func TestKeyPartsAllMiss(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	base := testKey("AAPL", day)
	if err := s.Put(ctx, base, json.RawMessage(`{"a":1}`), time.Second); err != nil {
		t.Fatal(err)
	}

	// Anything that could change the answer must miss.
	others := map[string]Key{
		"different day":     testKey("AAPL", day.AddDate(0, 0, 1)),
		"different symbol":  testKey("NVDA", day),
		"different paths":   {Symbol: "AAPL", AsOf: day, Paths: 50000, Seed: 1, ModelVersion: "v1"},
		"different seed":    {Symbol: "AAPL", AsOf: day, Paths: 20000, Seed: 7, ModelVersion: "v1"},
		"different version": {Symbol: "AAPL", AsOf: day, Paths: 20000, Seed: 1, ModelVersion: "v2"},
	}
	for name, key := range others {
		if _, ok, err := s.Get(ctx, key, 0); err != nil || ok {
			t.Errorf("%s should miss the cache (ok=%v err=%v)", name, ok, err)
		}
	}
}

func TestPutReplaces(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	key := testKey("AAPL", time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC))
	if err := s.Put(ctx, key, json.RawMessage(`{"score":1}`), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, key, json.RawMessage(`{"score":2}`), time.Second); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Get(ctx, key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"score":2}` {
		t.Fatalf("got %s, want the newer value", got)
	}
	st, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Analyses != 1 {
		t.Fatalf("replace left %d rows, want 1", st.Analyses)
	}
}

func TestRequestsAndStats(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	if err := s.Put(ctx, testKey("AAPL", day), json.RawMessage(`{}`), 4*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, testKey("NVDA", day), json.RawMessage(`{}`), 6*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, cached := range []bool{false, true, true} {
		if err := s.RecordRequest(ctx, "AAPL", cached); err != nil {
			t.Fatal(err)
		}
	}
	st, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Analyses != 2 || st.Symbols != 2 {
		t.Errorf("analyses=%d symbols=%d", st.Analyses, st.Symbols)
	}
	if st.Requests != 3 || st.CacheHits != 2 {
		t.Errorf("requests=%d hits=%d", st.Requests, st.CacheHits)
	}
	if st.MeanComputeMS != 5000 {
		t.Errorf("mean compute = %v, want 5000", st.MeanComputeMS)
	}
	if st.NewestAsOf != "2026-09-17" {
		t.Errorf("newest = %q", st.NewestAsOf)
	}
}

func TestPrune(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	old := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	for _, d := range []time.Time{old, recent} {
		if err := s.Put(ctx, testKey("AAPL", d), json.RawMessage(`{}`), time.Second); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.Prune(ctx, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d rows, want 1", n)
	}
	if _, ok, _ := s.Get(ctx, testKey("AAPL", recent), 0); !ok {
		t.Error("prune removed a recent analysis")
	}
}

func TestNilStoreIsUsable(t *testing.T) {
	var s *Store
	ctx := context.Background()
	if _, ok, err := s.Get(ctx, testKey("AAPL", time.Now()), 0); ok || err != nil {
		t.Errorf("nil store Get: ok=%v err=%v", ok, err)
	}
	if err := s.Put(ctx, testKey("AAPL", time.Now()), json.RawMessage(`{}`), 0); err != nil {
		t.Errorf("nil store Put: %v", err)
	}
	if err := s.RecordRequest(ctx, "AAPL", false); err != nil {
		t.Errorf("nil store RecordRequest: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("nil store Close: %v", err)
	}
}

func TestOpenReadOnlyReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ro.duckdb")
	day := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Put(context.Background(), testKey("AAPL", day), json.RawMessage(`{"score":3}`), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, ok, err := reader.Get(context.Background(), testKey("AAPL", day), 0)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if string(got) != `{"score":3}` {
		t.Fatalf("got %s", got)
	}
}

func TestOpenReportsALockedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked.duckdb")
	holder, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	// Force the write lock to be taken before the second open.
	if err := holder.Put(context.Background(), testKey("AAPL", time.Now()), json.RawMessage(`{}`), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second open returned %v, want ErrLocked", err)
	}
}

func TestRequestsHoldNoIdentifier(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.RecordRequest(ctx, "AAPL", false); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT column_name FROM information_schema.columns WHERE table_name = 'requests'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	for _, name := range columns {
		switch name {
		case "email", "ip", "user", "name", "address":
			t.Errorf("requests carries an identifying column: %s", name)
		}
	}
	if len(columns) != 3 {
		t.Errorf("columns = %v, want exactly requested_at, symbol and cached", columns)
	}
}

func TestOpenErasesAddressesFromAnOlderDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.duckdb")

	// Build a database in the shape an earlier version wrote.
	legacy, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE requests (
			requested_at TIMESTAMP NOT NULL,
			email        TEXT      NOT NULL,
			symbol       TEXT      NOT NULL,
			cached       BOOLEAN   NOT NULL
		);
		INSERT INTO requests VALUES (now(), 'someone@example.com', 'AAPL', false);`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var withEmail int
	if err := s.db.QueryRow(`
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'requests' AND column_name = 'email'`).Scan(&withEmail); err != nil {
		t.Fatal(err)
	}
	if withEmail != 0 {
		t.Error("opening an older database left the email column in place")
	}
	var rows int
	if err := s.db.QueryRow(`SELECT count(*) FROM requests`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("%d old request rows survived; the addresses should be gone", rows)
	}
	// The cache itself must be untouched by the migration.
	if err := s.RecordRequest(context.Background(), "AAPL", true); err != nil {
		t.Fatal(err)
	}
}

func TestGetExpiresStaleEntries(t *testing.T) {
	db := testStore(t)
	key := Key{Symbol: "AAPL", AsOf: time.Now(), Paths: 100, Seed: 1, ModelVersion: "v"}
	if err := db.Put(t.Context(), key, json.RawMessage(`{"symbol":"AAPL"}`), time.Second); err != nil {
		t.Fatal(err)
	}
	// Any age is acceptable when no maximum is asked for.
	if _, ok, err := db.Get(t.Context(), key, 0); err != nil || !ok {
		t.Fatalf("fresh entry missing with no max age: ok=%v err=%v", ok, err)
	}
	if _, ok, err := db.Get(t.Context(), key, time.Hour); err != nil || !ok {
		t.Fatalf("entry written moments ago read as stale: ok=%v err=%v", ok, err)
	}
	// A window shorter than the entry's age reports a miss, so the caller
	// recomputes rather than serving a number the market has moved past.
	if _, ok, err := db.Get(t.Context(), key, time.Nanosecond); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Error("a stale entry was served")
	}
}

func TestEvictKeepsTheMostRecentlyUsed(t *testing.T) {
	db := testStore(t)
	now := time.Now()
	for _, sym := range []string{"AAA", "BBB", "CCC", "DDD"} {
		key := Key{Symbol: sym, AsOf: now, Paths: 100, Seed: 1, ModelVersion: "v"}
		if err := db.Put(t.Context(), key, json.RawMessage(`{}`), time.Second); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Touching AAA makes it the most recently used despite being written
	// first, which is the whole point of evicting by use rather than age.
	if _, ok, err := db.Get(t.Context(), Key{Symbol: "AAA", AsOf: now, Paths: 100, Seed: 1, ModelVersion: "v"}, 0); err != nil || !ok {
		t.Fatalf("AAA missing: ok=%v err=%v", ok, err)
	}

	n, err := db.Evict(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("evicted %d rows, want 2", n)
	}
	for sym, want := range map[string]bool{"AAA": true, "DDD": true, "BBB": false, "CCC": false} {
		_, ok, err := db.Get(t.Context(), Key{Symbol: sym, AsOf: now, Paths: 100, Seed: 1, ModelVersion: "v"}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if ok != want {
			t.Errorf("%s present = %v, want %v", sym, ok, want)
		}
	}
}
