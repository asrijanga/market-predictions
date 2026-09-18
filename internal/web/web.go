// Package web serves the terminal front end and streams an analysis to it.
//
// The analysis takes seconds, so results are delivered over server-sent
// events: the client receives a stage event as each part of the pipeline
// begins and a result event at the end. That lets the interface show what
// the model is actually doing rather than an invented progress bar.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/asrijanga/market-predictions/internal/model"
	"github.com/asrijanga/market-predictions/internal/pack"
)

//go:embed static
var staticFiles embed.FS

// Assets returns the embedded front end, rooted at the directory the page
// is served from.
func Assets() fs.FS {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(fmt.Sprintf("web: embedded assets: %v", err))
	}
	return sub
}

// WriteStatic copies the front end to dir, which is how the static site is
// assembled for hosting somewhere that cannot run the analysis itself.
func WriteStatic(dir string) error {
	return fs.WalkDir(Assets(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, path)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := fs.ReadFile(Assets(), path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o644)
	})
}

// symbolPattern is what the front end is allowed to ask about: a plain
// ticker, optionally with a share-class suffix.
var symbolPattern = regexp.MustCompile(`^[A-Z]{1,6}([.\-][A-Z])?$`)

// Progress reports one stage of the pipeline.
type Progress func(stage string, fraction float64)

// Analyzer runs the pipeline for one symbol, reporting progress as it goes.
//
// A symbol is the whole of the request on purpose. The machine asks for
// nothing about the person asking, so there is no identifier to pass here,
// store, or later have to justify holding.
type Analyzer func(ctx context.Context, symbol string, p Progress) (*View, error)

// Server wires the static assets to an analyzer.
type Server struct {
	Analyze Analyzer
	// MaxConcurrent bounds how many analyses run at once, so a page left
	// hammering reload cannot spawn unbounded work.
	MaxConcurrent int
	// RateBurst and RateRefill bound how much work one visitor can ask
	// for: RateBurst analyses at once, then one more every RateRefill.
	// Zero leaves the endpoint open, which is right for a local run.
	RateBurst  int
	RateRefill time.Duration
	// AllowOrigin answers cross-origin calls from a front end hosted
	// somewhere else, such as the published static site. Empty allows none.
	AllowOrigin string
	// Cached reports whether a symbol can be answered without computing.
	// Reading a stored answer costs nothing, so it is never rationed; the
	// limit exists to protect the CPU and the upstream data provider, and
	// neither is touched by a cache hit.
	Cached func(ctx context.Context, symbol string) bool

	sem  chan struct{}
	rate *limiter
}

// Handler returns the routes: the front end at the root, the stream at
// /api/analyze.
func (s *Server) Handler() http.Handler {
	if s.MaxConcurrent <= 0 {
		s.MaxConcurrent = 4
	}
	s.sem = make(chan struct{}, s.MaxConcurrent)
	if s.RateBurst > 0 {
		refill := s.RateRefill
		if refill <= 0 {
			refill = time.Minute
		}
		s.rate = newLimiter(s.RateBurst, refill)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(Assets())))
	mux.HandleFunc("/api/analyze", s.handleAnalyze)
	return mux
}

func (s *Server) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	if s.AllowOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", s.AllowOrigin)
		w.Header().Set("Vary", "Origin")
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	symbol := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("symbol")))
	if !symbolPattern.MatchString(symbol) {
		http.Error(w, "bad symbol", http.StatusBadRequest)
		return
	}

	// Only computation is rationed. An answer already in the store costs
	// nothing to serve, so it is never refused -- otherwise a visitor who
	// spent their budget could not read what is sitting there, which is
	// the opposite of what the limit is for.
	client := clientIP(r)
	charged := false
	if s.Cached == nil || !s.Cached(r.Context(), symbol) {
		if ok, wait := s.rate.allow(client); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			http.Error(w, "too many new analyses from this address; try again in "+
				wait.Round(time.Second).String()+". Symbols already computed stay available.",
				http.StatusTooManyRequests)
			return
		}
		charged = true
	}
	// The answer can still arrive from the store if another request
	// computed it while this one waited for a slot, so hand the token back.
	defer func() {
		if charged {
			s.rate.refund(client)
		}
	}()
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	send := func(event string, payload any) {
		body, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
		flusher.Flush()
	}

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-r.Context().Done():
		return
	case <-time.After(30 * time.Second):
		send("error", map[string]string{"message": "server busy, try again"})
		return
	}

	send("stage", stageEvent{Stage: "contacting exchange", Fraction: 0.02})
	view, err := s.Analyze(r.Context(), symbol, func(stage string, fraction float64) {
		send("stage", stageEvent{Stage: stage, Fraction: fraction})
	})
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		log.Printf("web: %s: %v", symbol, err)
		send("error", map[string]string{"message": userMessage(symbol, err)})
		return
	}
	if view == nil || !view.Cached {
		charged = false // the work happened; the token stays spent
	}
	send("result", view)
}

type stageEvent struct {
	Stage    string  `json:"stage"`
	Fraction float64 `json:"fraction"`
}

// userMessage turns an internal failure into one line a terminal can show
// without leaking paths or stack detail.
func userMessage(symbol string, err error) string {
	text := err.Error()
	switch {
	case strings.Contains(text, "symbol not found"):
		return "NO SUCH SYMBOL: " + symbol
	case strings.Contains(text, "no usable option expiries"), strings.Contains(text, "no liquid call contracts"):
		return symbol + " HAS NO LIQUID LISTED OPTIONS"
	case strings.Contains(text, "bars of history"), strings.Contains(text, "closes"):
		return symbol + " HAS TOO LITTLE PRICE HISTORY"
	case strings.Contains(text, "context deadline exceeded"):
		return "TIMED OUT FETCHING DATA FOR " + symbol
	default:
		return "ANALYSIS FAILED FOR " + symbol
	}
}

// View is everything the terminal draws, shaped for display rather than for
// further computation.
type View struct {
	Symbol  string       `json:"symbol"`
	AsOf    string       `json:"asOf"`
	Spot    float64      `json:"spot"`
	Stance  string       `json:"stance"`
	Score   float64      `json:"score"`
	For     []SignalView `json:"for"`
	Against []SignalView `json:"against"`
	// Fair is what the company looks worth on what it reports; Timing is
	// how long the market has historically taken to agree.
	Fair        *FairView   `json:"fair,omitempty"`
	Timing      *TimingView `json:"timing,omitempty"`
	Earnings    string      `json:"earnings,omitempty"`
	ImpliedMove float64     `json:"impliedMove"`
	Crush       float64     `json:"crush"`
	BaseVol     float64     `json:"baseVol"`
	Cached      bool        `json:"cached"`
	Warnings    []string    `json:"warnings,omitempty"`
	Elapsed     string      `json:"elapsed"`
}

// SignalView is one piece of evidence.
type SignalView struct {
	Name   string  `json:"name"`
	Detail string  `json:"detail"`
	Score  float64 `json:"score"`
	Weight float64 `json:"weight"`
}

// FairView is what the shares look worth, and how that was reached.
type FairView struct {
	Value      float64      `json:"value"`
	Upside     float64      `json:"upside"`
	Confidence string       `json:"confidence"`
	Discount   float64      `json:"discount"`
	Growth     float64      `json:"growth"`
	Spread     float64      `json:"spread"`
	TrailingPE float64      `json:"trailingPE,omitempty"`
	ForwardPE  float64      `json:"forwardPE,omitempty"`
	Methods    []MethodView `json:"methods,omitempty"`
}

// MethodView is one valuation model's answer.
type MethodView struct {
	Name   string  `json:"name"`
	Value  float64 `json:"value"`
	Weight float64 `json:"weight"`
	Note   string  `json:"note"`
}

// TimingView is how long the gap has historically taken to close.
type TimingView struct {
	HalfLifeMonths float64 `json:"halfLifeMonths"`
	MedianMonths   float64 `json:"medianMonths"`
	WithinYear     float64 `json:"withinYear"`
}

// NewView condenses a model result into what the screen shows.
func NewView(p *pack.Pack, r *model.Result) *View {
	v := &View{
		Symbol: r.Symbol, AsOf: r.AsOf.Format(time.DateOnly), Spot: r.Spot,
		Stance: r.Stance, Score: r.Score, ImpliedMove: r.ImpliedMove,
		BaseVol: r.IV.BaseVol, Warnings: modelWarnings(r.Warnings), Elapsed: r.Elapsed,
	}
	if r.EarningsIdx > 0 {
		v.Earnings = r.EarningsDate.Format(time.DateOnly)
		v.Crush = r.IV.CrushPct(21)
	}
	for _, s := range r.Signals {
		sv := SignalView{Name: s.Name, Detail: s.Detail, Score: s.Score, Weight: s.Weight}
		switch {
		case s.Contribution > 0 && len(v.For) < 5:
			v.For = append(v.For, sv)
		case s.Contribution < 0 && len(v.Against) < 5:
			v.Against = append(v.Against, sv)
		}
	}
	if r.FairValue > 0 {
		f := &FairView{
			Value: r.FairValue, Upside: r.Upside,
			Confidence: r.Valuation.Confidence,
			Spread:     r.Valuation.Spread,
			Discount:   r.Valuation.DiscountRate,
			Growth:     r.Valuation.Growth,
			TrailingPE: r.Valuation.TrailingPE,
			ForwardPE:  r.Valuation.ForwardPE,
		}
		for _, e := range r.Valuation.Estimates {
			f.Methods = append(f.Methods, MethodView{
				Name: e.Method, Value: e.Value, Weight: e.Weight, Note: e.Note,
			})
		}
		v.Fair = f
	}
	// A timing estimate is only offered where the multiple has actually
	// reverted; without that there is no half-life to quote and a number
	// would be invention.
	if rev := r.Reversion; rev.Fitted && rev.MedianMonths > 0 {
		v.Timing = &TimingView{
			HalfLifeMonths: rev.HalfLife,
			MedianMonths:   rev.MedianMonths,
			WithinYear:     rev.WithinYear,
		}
	}
	return v
}

func modelWarnings(all []string) []string {
	var out []string
	for _, w := range all {
		if strings.HasPrefix(w, "news:") || strings.HasPrefix(w, "filings:") {
			continue
		}
		// The screen says this in its own words under CALL PLAN, so
		// repeating it as a note would just spend two more lines.
		if strings.HasPrefix(w, "no call contracts clear") {
			continue
		}
		out = append(out, w)
	}
	return out
}

// WriteConfig tells a published front end where to send its analyses.
//
// A static host cannot run the model and cannot reach the data providers
// either, because none of them send CORS headers. Naming a server that can
// is what lets the published page compute on demand instead of serving
// answers baked in at build time.
func WriteConfig(dir, api string) error {
	body, err := json.Marshal(struct {
		API string `json:"api"`
	}{API: strings.TrimRight(api, "/")})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), body, 0o644)
}
