package web

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
	srv := testServer(func(ctx context.Context, req Request, p Progress) (*View, error) {
		if req.Symbol != "AAPL" {
			t.Errorf("symbol = %q, want AAPL uppercased", req.Symbol)
		}
		if req.Email != "trader@example.com" {
			t.Errorf("email = %q, not passed through", req.Email)
		}
		p("fitting model", 0.4)
		p("simulating paths", 0.8)
		return &View{Symbol: req.Symbol, Stance: "bullish", Score: 0.5}, nil
	})
	defer srv.Close()

	events, data := readEvents(t, srv.URL+"/api/analyze?symbol=aapl&email=trader@example.com")
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
	srv := testServer(func(context.Context, Request, Progress) (*View, error) {
		t.Fatal("analyzer should not run for a bad symbol")
		return nil, nil
	})
	defer srv.Close()
	for _, bad := range []string{"", "TOOLONGSYM", "../etc", "A B", "AAPL;DROP"} {
		resp, err := http.Get(srv.URL + "/api/analyze?symbol=" + bad + "&email=a@b.com")
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
	srv := testServer(func(context.Context, Request, Progress) (*View, error) {
		return nil, errors.New("nasdaq: NOPE: symbol not found")
	})
	defer srv.Close()
	events, data := readEvents(t, srv.URL+"/api/analyze?symbol=NOPE&email=a@b.com")
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
	srv := testServer(func(context.Context, Request, Progress) (*View, error) { return nil, nil })
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
	s := &Server{MaxConcurrent: 2, Analyze: func(ctx context.Context, req Request, p Progress) (*View, error) {
		started <- struct{}{}
		<-release
		return &View{Symbol: req.Symbol}, nil
	}}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	for i := 0; i < 4; i++ {
		go func() {
			resp, err := http.Get(srv.URL + "/api/analyze?symbol=AAPL&email=a@b.com")
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

func TestWriteStaticCopiesTheFrontEnd(t *testing.T) {
	dir := t.TempDir()
	if err := WriteStatic(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.html", "app.js", "terminal.js", "crt.js", "loading.js", "report.js", "vendor/three.module.min.js"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("missing %s: %v", name, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", name)
		}
	}
	// The page must reference its assets relatively, or a project site
	// hosted under /repo/ cannot find them.
	body, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	if strings.Contains(page, `src="/`) || strings.Contains(page, `"/vendor/`) {
		t.Error("index.html uses absolute asset paths, which break under a subpath")
	}
	if !strings.Contains(page, "./vendor/three.module.min.js") {
		t.Error("index.html does not point at the vendored three.js")
	}
}

func TestShortDetailFitsTheScreen(t *testing.T) {
	cases := []string{
		"0.06 volatility points per unit of log-moneyness",
		"20-day realised volatility at the 71st percentile of the year",
		"-24.5% excess return over six months",
		"+16.3% away from it",
		"trading above it",
	}
	for _, in := range cases {
		got := shortDetail(in)
		if len(got) > 34 {
			t.Errorf("shortDetail(%q) = %q, still %d characters", in, got, len(got))
		}
	}
}

func TestModelWarningsDropDataNotes(t *testing.T) {
	got := modelWarnings([]string{
		"news: disabled",
		"filings: disabled (no SEC contact email)",
		"no earnings date available; timing ignores event risk",
	})
	if len(got) != 1 || !strings.Contains(got[0], "earnings") {
		t.Fatalf("got %v", got)
	}
}

func TestAnalyzeRequiresAnEmail(t *testing.T) {
	srv := testServer(func(context.Context, Request, Progress) (*View, error) {
		t.Fatal("analyzer should not run without an email")
		return nil, nil
	})
	defer srv.Close()
	for _, bad := range []string{"", "nobody", "no@domain", "a b@example.com", "@example.com"} {
		resp, err := http.Get(srv.URL + "/api/analyze?symbol=AAPL&email=" + url.QueryEscape(bad))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("email %q returned %d, want 400", bad, resp.StatusCode)
		}
	}
}

func TestValidEmail(t *testing.T) {
	good := []string{"a@b.co", "first.last@example.com", "x+tag@sub.example.co.uk"}
	bad := []string{"", "nobody", "a@b", "a b@example.com", "two@@example.com", strings.Repeat("x", 250) + "@example.com"}
	for _, e := range good {
		if !ValidEmail(e) {
			t.Errorf("%q should be accepted", e)
		}
	}
	for _, e := range bad {
		if ValidEmail(e) {
			t.Errorf("%q should be rejected", e)
		}
	}
}
