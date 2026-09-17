package web

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asrijanga/market-predictions/internal/model"
	"github.com/asrijanga/market-predictions/internal/pack"
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

func TestWriteStaticCopiesTheFrontEnd(t *testing.T) {
	dir := t.TempDir()
	if err := WriteStatic(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.html", "style.css", "app.js", "report.js"} {
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
	if strings.Contains(page, `src="/`) || strings.Contains(page, `href="/`) {
		t.Error("index.html uses absolute asset paths, which break under a subpath")
	}
	for _, ref := range []string{"./style.css", "./app.js"} {
		if !strings.Contains(page, ref) {
			t.Errorf("index.html does not reference %s", ref)
		}
	}
	// The front end is plain text in a document now. Nothing should pull a
	// renderer back in: the whole point of the rewrite is that the report
	// reflows, scales with the reader's font size and can be selected.
	if strings.Contains(page, "three") || strings.Contains(page, "importmap") {
		t.Error("index.html still loads a 3D renderer")
	}
}

// The page has to be legible on a phone held in one hand, which is where
// the character-grid version failed: it could not reflow.
func TestPageIsBuiltForSmallScreens(t *testing.T) {
	dir := t.TempDir()
	if err := WriteStatic(dir); err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, want := range []string{
		"width=device-width",      // no fixed-width layout
		"viewport-fit=cover",      // notch-aware, with safe-area padding
		"An experiment, for fun.", // the disclaimer leads the page
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html is missing %q", want)
		}
	}
	// A page that says "for fun" after the numbers is not a disclaimer.
	if i, j := strings.Index(html, "An experiment, for fun."), strings.Index(html, "search-form"); i < 0 || i > j {
		t.Error("the disclaimer does not come before the search")
	}

	css, err := os.ReadFile(filepath.Join(dir, "style.css"))
	if err != nil {
		t.Fatal(err)
	}
	style := string(css)
	for _, want := range []string{
		"prefers-reduced-motion", // animation is opt-out
		"safe-area-inset",        // clears the home indicator
		"@media (min-width:",     // the layout actually responds
	} {
		if !strings.Contains(style, want) {
			t.Errorf("style.css is missing %q", want)
		}
	}
}

// The screen used to be 62 columns wide, so labels were abbreviated before
// they were sent. The page reflows, so it gets the model's own wording.
func TestViewKeepsTheModelsWording(t *testing.T) {
	r := &model.Result{
		Symbol: "TEST", AsOf: time.Now(), Spot: 100,
		Stance: "bullish", Score: 0.4,
		Signals: []model.Signal{{
			Name:   "Position against the 200-day average",
			Detail: "+16.3% away from it",
			Score:  0.5, Weight: 0.15, Contribution: 0.075,
		}},
	}
	v := NewView(&pack.Pack{}, r)
	if len(v.For) != 1 {
		t.Fatalf("got %d supporting signals", len(v.For))
	}
	if v.For[0].Name != "Position against the 200-day average" {
		t.Errorf("name = %q, want it unabridged", v.For[0].Name)
	}
	if v.For[0].Detail != "+16.3% away from it" {
		t.Errorf("detail = %q, want it unabridged", v.For[0].Detail)
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

func TestAnalyzeAsksForNothingButASymbol(t *testing.T) {
	srv := testServer(func(ctx context.Context, symbol string, p Progress) (*View, error) {
		return &View{Symbol: symbol}, nil
	})
	defer srv.Close()

	// A request carrying an identifier still works, and the identifier is
	// ignored rather than read, stored or required.
	resp, err := http.Get(srv.URL + "/api/analyze?symbol=AAPL&email=someone@example.com")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "someone@example.com") {
		t.Error("the response echoed an address back")
	}

	// The front end must not ask for one either.
	page, err := fs.ReadFile(Assets(), "app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"email", "localStorage"} {
		if strings.Contains(strings.ToLower(string(page)), strings.ToLower(banned)) {
			t.Errorf("app.js still refers to %q", banned)
		}
	}
}
