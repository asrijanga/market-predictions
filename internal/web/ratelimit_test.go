package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLimiterSpendsAndRefills(t *testing.T) {
	l := newLimiter(2, 50*time.Millisecond)

	for i := 0; i < 2; i++ {
		if ok, _ := l.allow("a"); !ok {
			t.Fatalf("request %d refused inside the burst", i+1)
		}
	}
	ok, wait := l.allow("a")
	if ok {
		t.Fatal("a third request was allowed past a burst of two")
	}
	if wait <= 0 || wait > 50*time.Millisecond {
		t.Errorf("wait = %v, want something inside one refill", wait)
	}

	// Another address has its own bucket.
	if ok, _ := l.allow("b"); !ok {
		t.Error("a different address was refused")
	}

	time.Sleep(60 * time.Millisecond)
	if ok, _ := l.allow("a"); !ok {
		t.Error("no token had been earned back after a refill period")
	}
}

func TestLimiterRefundsWork(t *testing.T) {
	l := newLimiter(1, time.Hour)
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("the first request was refused")
	}
	if ok, _ := l.allow("a"); ok {
		t.Fatal("the bucket was not empty")
	}
	l.refund("a")
	if ok, _ := l.allow("a"); !ok {
		t.Error("a refunded token was not available again")
	}
	// A refund cannot mint tokens beyond the burst.
	for i := 0; i < 5; i++ {
		l.refund("a")
	}
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("the refunded token vanished")
	}
	if ok, _ := l.allow("a"); ok {
		t.Error("refunds accumulated past the burst")
	}
}

// Behind Fly every visitor shares the proxy's address, so limiting on
// RemoteAddr would ration the whole site as one client.
func TestClientIPPrefersTheForwardedAddress(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		remote  string
		want    string
	}{
		{"fly header wins", map[string]string{
			"Fly-Client-IP": "203.0.113.7", "X-Forwarded-For": "198.51.100.1",
		}, "10.0.0.1:443", "203.0.113.7"},
		{"forwarded-for is read left to right", map[string]string{
			"X-Forwarded-For": "198.51.100.1, 10.0.0.9",
		}, "10.0.0.1:443", "198.51.100.1"},
		{"falls back to the socket", nil, "192.0.2.5:51234", "192.0.2.5"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/api/analyze?symbol=AAPL", nil)
		r.RemoteAddr = c.remote
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		if got := clientIP(r); got != c.want {
			t.Errorf("%s: clientIP = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAnalyzeRefusesAnAddressThatAsksTooMuch(t *testing.T) {
	computed := 0
	s := &Server{
		RateBurst:  1,
		RateRefill: time.Hour,
		Analyze: func(ctx context.Context, symbol string, p Progress) (*View, error) {
			computed++
			return &View{Symbol: symbol}, nil
		},
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	get := func() int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/analyze?symbol=AAPL", nil)
		req.Header.Set("Fly-Client-IP", "203.0.113.9")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := get(); code != http.StatusOK {
		t.Fatalf("first analysis returned %d", code)
	}
	if code := get(); code != http.StatusTooManyRequests {
		t.Errorf("second analysis returned %d, want 429", code)
	}
	if computed != 1 {
		t.Errorf("the model ran %d times; the refused request should not reach it", computed)
	}
}

// A cached answer costs nothing to serve, so browsing symbols somebody has
// already asked about must not use up the visitor's budget.
func TestCachedAnswersAreNotRationed(t *testing.T) {
	s := &Server{
		RateBurst:  1,
		RateRefill: time.Hour,
		Analyze: func(ctx context.Context, symbol string, p Progress) (*View, error) {
			return &View{Symbol: symbol, Cached: true}, nil
		},
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/analyze?symbol=AAPL", nil)
		req.Header.Set("Fly-Client-IP", "203.0.113.10")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("cached read %d returned %d, want 200", i+1, resp.StatusCode)
		}
	}
}

// A visitor who has spent their budget must still be able to read answers
// that are already stored: the limit protects the CPU and the upstream
// provider, and a cache hit touches neither.
func TestSpentBudgetStillReadsTheCache(t *testing.T) {
	stored := map[string]bool{"AAPL": true}
	s := &Server{
		RateBurst:  1,
		RateRefill: time.Hour,
		Cached: func(ctx context.Context, symbol string) bool {
			return stored[symbol]
		},
		Analyze: func(ctx context.Context, symbol string, p Progress) (*View, error) {
			return &View{Symbol: symbol, Cached: stored[symbol]}, nil
		},
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	get := func(symbol string) int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/analyze?symbol="+symbol, nil)
		req.Header.Set("Fly-Client-IP", "203.0.113.11")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := get("MSFT"); code != http.StatusOK {
		t.Fatalf("the one allowed analysis returned %d", code)
	}
	if code := get("NVDA"); code != http.StatusTooManyRequests {
		t.Fatalf("a second new analysis returned %d, want 429", code)
	}
	// Budget spent, but this one is on file.
	for i := 0; i < 3; i++ {
		if code := get("AAPL"); code != http.StatusOK {
			t.Fatalf("cached read %d returned %d with the budget spent, want 200", i+1, code)
		}
	}
}
