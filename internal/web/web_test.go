package web

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testServer(a Analyzer) *httptest.Server {
	s := &Server{Analyze: a}
	return httptest.NewServer(s.Handler())
}

func readEvents(t *testing.T, url string) (events []string, data []string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if name, ok := strings.CutPrefix(line, "event: "); ok {
			events = append(events, name)
		}
		if payload, ok := strings.CutPrefix(line, "data: "); ok {
			data = append(data, payload)
		}
	}
	return events, data
}

func TestAnalyzeStreamsStagesThenResult(t *testing.T) {
	srv := testServer(func(ctx context.Context, symbol string, p Progress) (*View, error) {
		if symbol != "AAPL" {
			t.Errorf("symbol = %q, want AAPL uppercased", symbol)
		}
		p("fitting model", 0.4)
		p("simulating paths", 0.8)
		return &View{Symbol: symbol, Stance: "bullish", Score: 0.5}, nil
	})
	defer srv.Close()

	events, data := readEvents(t, srv.URL+"/api/analyze?symbol=aapl")
	if len(events) != 4 {
		t.Fatalf("events = %v, want three stages and one result", events)
	}
	if events[len(events)-1] != "result" {
		t.Errorf("last event = %q, want result", events[len(events)-1])
	}
	for _, e := range events[:3] {
		if e != "stage" {
			t.Errorf("expected stage events first, got %q", e)
		}
	}
	if !strings.Contains(data[len(data)-1], `"stance":"bullish"`) {
		t.Errorf("result payload = %s", data[len(data)-1])
	}
	if !strings.Contains(data[1], `"fitting model"`) {
		t.Errorf("stage payload = %s", data[1])
	}
}

func TestAnalyzeRejectsBadSymbols(t *testing.T) {
	srv := testServer(func(context.Context, string, Progress) (*View, error) {
		t.Fatal("analyzer should not run for a bad symbol")
		return nil, nil
	})
	defer srv.Close()
	for _, bad := range []string{"", "TOOLONGSYM", "../etc", "A B", "AAPL;DROP"} {
		resp, err := http.Get(srv.URL + "/api/analyze?symbol=" + bad)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("symbol %q returned %d, want 400", bad, resp.StatusCode)
		}
	}
}

func TestAnalyzeSendsErrorEvent(t *testing.T) {
	srv := testServer(func(context.Context, string, Progress) (*View, error) {
		return nil, errors.New("nasdaq: NOPE: symbol not found")
	})
	defer srv.Close()
	events, data := readEvents(t, srv.URL+"/api/analyze?symbol=NOPE")
	if events[len(events)-1] != "error" {
		t.Fatalf("events = %v, want an error last", events)
	}
	if !strings.Contains(data[len(data)-1], "NO SUCH SYMBOL: NOPE") {
		t.Errorf("payload = %s", data[len(data)-1])
	}
}

func TestUserMessageStaysReadable(t *testing.T) {
	cases := map[string]string{
		"nasdaq: symbol not found":             "NO SUCH SYMBOL: XYZ",
		"model: no usable option expiries":     "XYZ HAS NO LIQUID LISTED OPTIONS",
		"XYZ: 40 bars of history, need 100":    "XYZ HAS TOO LITTLE PRICE HISTORY",
		"context deadline exceeded":            "TIMED OUT FETCHING DATA FOR XYZ",
		"some internal detail /home/user/x.go": "ANALYSIS FAILED FOR XYZ",
	}
	for in, want := range cases {
		if got := userMessage("XYZ", errors.New(in)); got != want {
			t.Errorf("userMessage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIndexIsServed(t *testing.T) {
	srv := testServer(func(context.Context, string, Progress) (*View, error) { return nil, nil })
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestConcurrencyIsBounded(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	s := &Server{MaxConcurrent: 2, Analyze: func(ctx context.Context, symbol string, p Progress) (*View, error) {
		started <- struct{}{}
		<-release
		return &View{Symbol: symbol}, nil
	}}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	for i := 0; i < 4; i++ {
		go func() {
			resp, err := http.Get(srv.URL + "/api/analyze?symbol=AAPL")
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	// Two analyses should start; the rest wait for a slot.
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("expected two analyses to start")
		}
	}
	select {
	case <-started:
		t.Fatal("a third analysis started despite the limit of two")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
}
