package edgar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRecentFilings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua != "mktpredict/1.0 (me@example.com)" {
			t.Errorf("User-Agent = %q", ua)
		}
		switch r.URL.Path {
		case "/files/company_tickers.json":
			w.Write([]byte(`{"0":{"cik_str":320193,"ticker":"AAPL","title":"Apple Inc."}}`))
		case "/submissions/CIK0000320193.json":
			w.Write([]byte(`{"filings":{"recent":{"form":["10-Q","8-K","4","8-K"],"filingDate":["2026-07-31","2026-07-30","2026-07-29","2025-01-01"],"items":["","2.02,9.01","",""],"primaryDocDescription":["10-Q","8-K","4","8-K"]}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), nil, "me@example.com")
	c.SECURL, c.DataURL = srv.URL, srv.URL
	got, err := c.RecentFilings(context.Background(), "aapl", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Form != "10-Q" || got[1].Items != "2.02,9.01" {
		t.Fatalf("got %+v", got)
	}
}

func TestRequiresContact(t *testing.T) {
	c := NewClient(http.DefaultClient, nil, "")
	if _, err := c.RecentFilings(context.Background(), "AAPL", time.Time{}, time.Now()); !errors.Is(err, ErrNoContact) {
		t.Fatalf("err = %v", err)
	}
}
