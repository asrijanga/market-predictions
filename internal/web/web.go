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
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
	sem           chan struct{}
}

// Handler returns the routes: the front end at the root, the stream at
// /api/analyze.
func (s *Server) Handler() http.Handler {
	if s.MaxConcurrent <= 0 {
		s.MaxConcurrent = 4
	}
	s.sem = make(chan struct{}, s.MaxConcurrent)

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(Assets())))
	mux.HandleFunc("/api/analyze", s.handleAnalyze)
	return mux
}

func (s *Server) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("symbol")))
	if !symbolPattern.MatchString(symbol) {
		http.Error(w, "bad symbol", http.StatusBadRequest)
		return
	}
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
	Symbol      string        `json:"symbol"`
	AsOf        string        `json:"asOf"`
	Spot        float64       `json:"spot"`
	Stance      string        `json:"stance"`
	Score       float64       `json:"score"`
	Outlooks    []OutlookView `json:"outlooks"`
	For         []SignalView  `json:"for"`
	Against     []SignalView  `json:"against"`
	Plan        *PlanView     `json:"plan,omitempty"`
	Earnings    string        `json:"earnings,omitempty"`
	ImpliedMove float64       `json:"impliedMove"`
	Crush       float64       `json:"crush"`
	BaseVol     float64       `json:"baseVol"`
	Cached      bool          `json:"cached"`
	Warnings    []string      `json:"warnings,omitempty"`
	Elapsed     string        `json:"elapsed"`
}

// OutlookView is one forecast horizon.
type OutlookView struct {
	Name      string  `json:"name"`
	Date      string  `json:"date"`
	Direction string  `json:"direction"`
	ProbUp    float64 `json:"probUp"`
	Median    float64 `json:"median"`
	Mean      float64 `json:"mean"`
	Low       float64 `json:"low"`
	High      float64 `json:"high"`
}

// SignalView is one piece of evidence.
type SignalView struct {
	Name   string  `json:"name"`
	Detail string  `json:"detail"`
	Score  float64 `json:"score"`
	Weight float64 `json:"weight"`
}

// PlanView is the best entry plan.
type PlanView struct {
	Trigger    string  `json:"trigger"`
	Window     string  `json:"window"`
	Expiry     string  `json:"expiry"`
	Strike     float64 `json:"strike"`
	FillProb   float64 `json:"fillProb"`
	MeanReturn float64 `json:"meanReturn"`
	ProbProfit float64 `json:"probProfit"`
	BreakEven  string  `json:"breakEven"`
	Cost       float64 `json:"cost"`
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
	for _, o := range r.Outlooks {
		v.Outlooks = append(v.Outlooks, OutlookView{
			Name: o.Name, Date: o.Date.Format(time.DateOnly), Direction: o.Direction,
			ProbUp: o.ProbUp, Median: o.MedianReturn, Mean: o.MeanReturn, Low: o.Low, High: o.High,
		})
	}
	for _, s := range r.Signals {
		sv := SignalView{Name: shortSignalName(s.Name), Detail: shortDetail(s.Detail), Score: s.Score, Weight: s.Weight}
		switch {
		case s.Contribution > 0 && len(v.For) < 5:
			v.For = append(v.For, sv)
		case s.Contribution < 0 && len(v.Against) < 5:
			v.Against = append(v.Against, sv)
		}
	}
	if len(r.Strategies) > 0 {
		b := r.Strategies[0]
		v.Plan = &PlanView{
			Trigger: triggerLabel(b.Trigger, r.Spot),
			Window:  b.Window.Start.Format("Jan 2") + " to " + b.Window.End.Format("Jan 2"),
			Expiry:  b.Contract.Expiry.Format(time.DateOnly), Strike: b.Contract.Strike,
			FillProb: b.Stats.PTrade, MeanReturn: b.Stats.MeanReturn, ProbProfit: b.Stats.PProfit,
			BreakEven: breakEvenLabel(float64(b.BreakEvenDrift)), Cost: b.Stats.MeanCost,
		}
	}
	return v
}

// shortNames keep signal labels inside the terminal's 62 columns. An
// unmapped name falls through unchanged rather than being truncated here.
var shortNames = map[string]string{
	"Fitted trend":                            "TREND",
	"Position against the 200-day average":    "VS 200-DAY",
	"Position against the 50-day average":     "VS 50-DAY",
	"Relative strength against the benchmark": "REL STRENGTH",
	"One-month momentum":                      "1-MONTH MOVE",
	"Distance from the 52-week high":          "FROM 52W HIGH",
	"RSI(14)":                                 "RSI(14)",
	"Volatility regime":                       "VOL REGIME",
	"Option skew":                             "OPTION SKEW",
	"Put/call open interest":                  "PUT/CALL OI",
}

// verbosePhrases are the readings whose prose does not fit a 62-column
// screen. The report keeps the long form; the terminal gets the short one.
var verbosePhrases = strings.NewReplacer(
	"volatility points per unit of log-moneyness", "pts per log-moneyness",
	"20-day realised volatility at the", "20d vol at",
	"percentile of the year", "percentile",
	"excess return over six months", "excess over 6m",
	"over six months", "over 6m",
	"trading above it", "above",
	"trading below it", "below",
	"away from it", "away",
)

func shortDetail(detail string) string { return verbosePhrases.Replace(detail) }

func shortSignalName(name string) string {
	if short, ok := shortNames[name]; ok {
		return short
	}
	return strings.ToUpper(name)
}

// modelWarnings keeps only what changes the answer. Headlines and filings
// are part of the written brief, not of the model, so their absence is not
// worth a line on a screen this small.
func modelWarnings(all []string) []string {
	var out []string
	for _, w := range all {
		if strings.HasPrefix(w, "news:") || strings.HasPrefix(w, "filings:") {
			continue
		}
		out = append(out, w)
	}
	return out
}

func triggerLabel(t model.Trigger, spot float64) string {
	switch t.Kind {
	case model.TriggerDip:
		return fmt.Sprintf("DIP TO %.2f", spot*t.Level)
	case model.TriggerBreakout:
		return fmt.Sprintf("RISE TO %.2f", spot*t.Level)
	default:
		return "ENTER AT OPEN"
	}
}

func breakEvenLabel(d float64) string {
	switch {
	case math.IsInf(d, 1):
		return "OFF THE SCALE"
	case math.IsInf(d, -1):
		return "ANY DRIFT"
	default:
		return fmt.Sprintf("%+.1f%%/YR", 100*d)
	}
}
