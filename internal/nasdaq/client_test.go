package nasdaq

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asrijanga/market-predictions/internal/cache"
)

const historyBody = `{"data":{"symbol":"AAPL","totalRecords":3,"tradesTable":{"rows":[
{"date":"09/15/2026","close":"$331.34","volume":"31,748,180","open":"$330.135","high":"$331.78","low":"$328.35"},
{"date":"09/14/2026","close":"$333.08","volume":"39,269,150","open":"$334.79","high":"$335.50","low":"$331.34"},
{"date":"09/11/2026","close":"$332.27","volume":"50,716,870","open":"$327.45","high":"$336.22","low":"$326.30"}]}},
"status":{"rCode":200,"bCodeMessage":null}}`

const notFoundBody = `{"data":null,"message":null,"status":{"rCode":400,"bCodeMessage":[{"code":1001,"errorMessage":"Symbol not exists."}]}}`

func TestParseNumber(t *testing.T) {
	cases := map[string]float64{
		"$331.34":    331.34,
		"31,748,180": 31748180,
		"757.39":     757.39,
		"NA":         0,
		"":           0,
		"-0.21":      -0.21,
	}
	for in, want := range cases {
		if got := parseNumber(in); got != want {
			t.Errorf("parseNumber(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestHistoryParsesAndSortsOldestFirst(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent")
		}
		w.Write([]byte(historyBody))
	}))
	defer srv.Close()

	c := NewClient(2, nil)
	c.BaseURL = srv.URL
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	bars, err := c.History(context.Background(), "AAPL", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 3 {
		t.Fatalf("got %d bars, want 3", len(bars))
	}
	if !bars[0].Date.Before(bars[2].Date) {
		t.Errorf("bars not sorted oldest first: %v", bars)
	}
	if bars[2].Close != 331.34 || bars[2].Volume != 31748180 {
		t.Errorf("last bar = %+v", bars[2])
	}
}

func TestHistoryFallsBackToETF(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("assetclass") == "etf" {
			w.Write([]byte(historyBody))
			return
		}
		w.Write([]byte(notFoundBody))
	}))
	defer srv.Close()

	c := NewClient(2, nil)
	c.BaseURL = srv.URL
	bars, err := c.History(context.Background(), "SPY", time.Now().AddDate(0, -1, 0), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 3 || calls.Load() != 2 {
		t.Fatalf("bars=%d calls=%d", len(bars), calls.Load())
	}
}

func TestHistoryUnknownSymbol(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(notFoundBody))
	}))
	defer srv.Close()
	c := NewClient(2, nil)
	c.BaseURL = srv.URL
	_, err := c.History(context.Background(), "NOPE", time.Now().AddDate(0, -1, 0), time.Now())
	if !errors.Is(err, ErrSymbolNotFound) {
		t.Fatalf("err = %v, want ErrSymbolNotFound", err)
	}
}

func TestGetRetriesOn429AndCaches(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(historyBody))
	}))
	defer srv.Close()

	store, err := cache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(2, store)
	c.BaseURL = srv.URL
	ctx := context.Background()
	if _, err := c.get(ctx, srv.URL+"/x", "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.get(ctx, srv.URL+"/x", "k"); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("server calls = %d, want 2 (one 429, one success, then cache hit)", got)
	}
}
