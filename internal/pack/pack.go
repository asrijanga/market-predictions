// Package pack assembles everything an analyst needs to judge call timing
// for one symbol: a year of prices, trend statistics, the option chain,
// news, filings and the event calendar.
package pack

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/asrijanga/market-predictions/internal/edgar"
	"github.com/asrijanga/market-predictions/internal/market"
	"github.com/asrijanga/market-predictions/internal/nasdaq"
	"github.com/asrijanga/market-predictions/internal/news"
	"github.com/asrijanga/market-predictions/internal/options"
	"github.com/asrijanga/market-predictions/internal/quant"
)

// Event is a dated item on the forward calendar.
type Event struct {
	Date time.Time `json:"date"`
	Name string    `json:"name"`
}

// YearStats summarises the full lookback year.
type YearStats struct {
	Return1Y          float64 `json:"return_1y"`
	High52            float64 `json:"high_52w"`
	Low52             float64 `json:"low_52w"`
	FromHighPct       float64 `json:"from_52w_high_pct"`
	SMA200            float64 `json:"sma_200"`
	AboveSMA200       bool    `json:"above_sma_200"`
	RealizedVol20     float64 `json:"realized_vol_20d"`
	RealizedVol1Y     float64 `json:"realized_vol_1y"`
	RealizedVolPctl   float64 `json:"realized_vol_20d_percentile"`
	AvgDollarVolume   float64 `json:"avg_dollar_volume"`
	BenchmarkReturn1Y float64 `json:"benchmark_return_1y"`
}

// Pack is the complete data bundle for one symbol.
type Pack struct {
	Symbol      string    `json:"symbol"`
	AsOf        time.Time `json:"as_of"`
	Price       float64   `json:"price"`
	Benchmark   string    `json:"benchmark"`
	HorizonDays int       `json:"horizon_days"`
	TargetDate  time.Time `json:"target_date"`

	Trend  quant.Forecast `json:"trend_6m"`
	Year   YearStats      `json:"year"`
	Bars   []market.Bar   `json:"bars"`
	Weekly []market.Bar   `json:"weekly"`

	// ModelCloses is the full estimation sample, longer than Bars (the
	// display window), used to fit the volatility models.
	ModelCloses []float64 `json:"model_closes,omitempty"`

	EarningsDate   time.Time               `json:"earnings_date"`
	DaysToEarnings int                     `json:"days_to_earnings"`
	Filings        []market.Filing         `json:"filings"`
	Headlines      []market.Headline       `json:"headlines"`
	Expiries       []options.ExpirySummary `json:"expiries"`
	Events         []Event                 `json:"events"`

	Warnings []string `json:"warnings,omitempty"`
}

// Sources groups the data clients Build depends on. News and EDGAR may be
// nil, in which case those sections are skipped with a warning.
type Sources struct {
	Nasdaq *nasdaq.Client
	News   *news.Client
	Edgar  *edgar.Client
}

// Options tunes what Build collects.
type Options struct {
	Benchmark    string
	LookbackDays int // trading days of history to display (252 = 1y)
	ModelDays    int // trading days of history to fetch for model estimation
	TrendDays    int // window for the momentum model (126 = 6m)
	HorizonDays  int // projection horizon in trading days
	NewsDays     int // calendar days of headlines
	MaxHeadlines int
	RiskFreeRate float64
	Now          time.Time
}

// Build fetches every input concurrently and assembles the pack.
func Build(ctx context.Context, src Sources, symbol string, o Options) (*Pack, error) {
	now := o.Now
	fetchDays := max(o.LookbackDays, o.ModelDays)
	from := now.AddDate(0, 0, -(fetchDays*7/5 + 30))
	p := &Pack{Symbol: symbol, AsOf: now, Benchmark: o.Benchmark, HorizonDays: o.HorizonDays}
	p.TargetDate = now.AddDate(0, 0, o.HorizonDays*7/5)

	type result struct {
		bars, bench []market.Bar
		earnings    time.Time
		chain       []market.OptionQuote
		heads       []market.Headline
		filings     []market.Filing
		errs        map[string]error
	}
	r := result{errs: map[string]error{}}
	type job struct {
		name string
		run  func() error
	}
	jobs := []job{
		{"history", func() (err error) { r.bars, err = src.Nasdaq.History(ctx, symbol, from, now); return }},
		{"benchmark", func() (err error) { r.bench, err = src.Nasdaq.History(ctx, o.Benchmark, from, now); return }},
		{"earnings", func() (err error) { r.earnings, err = src.Nasdaq.EarningsDate(ctx, symbol, now); return }},
		{"options", func() (err error) {
			r.chain, err = src.Nasdaq.OptionChain(ctx, symbol, now, now.AddDate(0, 0, o.HorizonDays*7/5+45))
			return
		}},
	}
	if src.News != nil {
		jobs = append(jobs, job{"news", func() (err error) {
			r.heads, err = src.News.Headlines(ctx, symbol+" stock", now.AddDate(0, 0, -o.NewsDays), now)
			return
		}})
	} else {
		p.Warnings = append(p.Warnings, "news: disabled")
	}
	if src.Edgar != nil {
		jobs = append(jobs, job{"filings", func() (err error) {
			r.filings, err = src.Edgar.RecentFilings(ctx, symbol, now.AddDate(-1, 0, 0), now)
			return
		}})
	} else {
		p.Warnings = append(p.Warnings, "filings: disabled (no SEC contact email)")
	}

	errCh := make(chan struct {
		name string
		err  error
	}, len(jobs))
	for _, j := range jobs {
		go func(j job) {
			err := j.run()
			errCh <- struct {
				name string
				err  error
			}{j.name, err}
		}(j)
	}
	for range jobs {
		e := <-errCh
		if e.err != nil {
			r.errs[e.name] = e.err
		}
	}
	for _, must := range []string{"history", "benchmark"} {
		if err := r.errs[must]; err != nil {
			return nil, fmt.Errorf("%s: %w", must, err)
		}
	}
	for _, name := range []string{"earnings", "options", "news", "filings"} {
		if err := r.errs[name]; err != nil {
			p.Warnings = append(p.Warnings, name+": "+err.Error())
		}
	}
	if len(r.bars) < o.TrendDays {
		return nil, fmt.Errorf("%s: only %d bars of history", symbol, len(r.bars))
	}
	if len(r.bars) > fetchDays {
		r.bars = r.bars[len(r.bars)-fetchDays:]
	}
	p.ModelCloses = quant.Closes(r.bars)
	if len(r.bars) > o.LookbackDays {
		r.bars = r.bars[len(r.bars)-o.LookbackDays:]
	}
	if len(r.bench) > o.LookbackDays {
		r.bench = r.bench[len(r.bench)-o.LookbackDays:]
	}

	p.Bars = r.bars
	p.Weekly = weekly(r.bars)
	closes := quant.Closes(r.bars)
	p.Price = closes[len(closes)-1]

	bench := quant.SeriesStats(quant.Closes(r.bench), o.TrendDays)
	trend, err := quant.Analyze(symbol, r.bars, bench, o.TrendDays, o.HorizonDays)
	if err != nil {
		return nil, err
	}
	p.Trend = trend
	p.Year = yearStats(r.bars, r.bench)

	if !r.earnings.IsZero() {
		p.EarningsDate = r.earnings
		p.DaysToEarnings = quant.TradingDays(now, r.earnings)
	}
	p.Filings = r.filings
	if len(r.heads) > o.MaxHeadlines {
		r.heads = r.heads[:o.MaxHeadlines]
	}
	p.Headlines = r.heads
	p.Expiries = options.Summarize(r.chain, p.Price, now, o.RiskFreeRate)
	p.Events = calendar(now, p.TargetDate.AddDate(0, 0, 45), p.EarningsDate)
	return p, nil
}

func yearStats(bars, bench []market.Bar) YearStats {
	closes := quant.Closes(bars)
	y := YearStats{
		Return1Y:        quant.TotalReturn(closes, len(closes)-1),
		High52:          closes[0],
		Low52:           closes[0],
		SMA200:          quant.SMA(closes, 200),
		RealizedVol20:   options.RealizedVol(closes, 20),
		RealizedVol1Y:   options.RealizedVol(closes, len(closes)-1),
		RealizedVolPctl: options.RealizedVolPercentile(closes, 20),
	}
	var dollar float64
	for _, b := range bars {
		y.High52 = max(y.High52, b.High, b.Close)
		if b.Low > 0 {
			y.Low52 = min(y.Low52, b.Low)
		}
		dollar += b.Close * float64(b.Volume)
	}
	y.AvgDollarVolume = dollar / float64(len(bars))
	last := closes[len(closes)-1]
	y.FromHighPct = last/y.High52 - 1
	y.AboveSMA200 = y.SMA200 > 0 && last > y.SMA200
	if bc := quant.Closes(bench); len(bc) > 1 {
		y.BenchmarkReturn1Y = quant.TotalReturn(bc, len(bc)-1)
	}
	return y
}

// weekly compresses daily bars into calendar-week bars (Friday close).
func weekly(bars []market.Bar) []market.Bar {
	var out []market.Bar
	var cur market.Bar
	var curYear, curWeek int
	for _, b := range bars {
		yr, wk := b.Date.ISOWeek()
		if yr != curYear || wk != curWeek {
			if !cur.Date.IsZero() {
				out = append(out, cur)
			}
			cur = b
			curYear, curWeek = yr, wk
			continue
		}
		cur.Date = b.Date
		cur.Close = b.Close
		cur.High = max(cur.High, b.High)
		if b.Low > 0 {
			cur.Low = min(cur.Low, b.Low)
		}
		cur.Volume += b.Volume
	}
	if !cur.Date.IsZero() {
		out = append(out, cur)
	}
	return out
}

// FOMC meeting decision days (second day of each two-day meeting). Verify
// against federalreserve.gov when extending.
var fomcDecisionDays = []string{
	"2026-01-28", "2026-03-18", "2026-04-29", "2026-06-17", "2026-07-29", "2026-09-16", "2026-10-28", "2026-12-09",
	"2027-01-27", "2027-03-17", "2027-04-28", "2027-06-16", "2027-07-28", "2027-09-15", "2027-10-27", "2027-12-08",
}

func calendar(from, to, earnings time.Time) []Event {
	var ev []Event
	for _, d := range fomcDecisionDays {
		t, _ := time.Parse(time.DateOnly, d)
		if t.After(from) && !t.After(to) {
			ev = append(ev, Event{t, "FOMC rate decision"})
		}
	}
	for m := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC); !m.After(to); m = m.AddDate(0, 1, 0) {
		if tf := quant.ThirdFriday(m.Year(), m.Month()); tf.After(from) && !tf.After(to) {
			ev = append(ev, Event{tf, "Monthly options expiration"})
		}
	}
	if !earnings.IsZero() && earnings.After(from) {
		ev = append(ev, Event{earnings, "Earnings report (estimated)"})
	}
	sort.Slice(ev, func(i, j int) bool { return ev[i].Date.Before(ev[j].Date) })
	return ev
}

// Write stores pack.json and pack.md under dir/SYMBOL and returns that path.
func Write(dir string, p *Pack) (string, error) {
	out := filepath.Join(dir, p.Symbol)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", err
	}
	js, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(out, "pack.json"), js, 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(out, "pack.md"), []byte(Render(p)), 0o644); err != nil {
		return "", err
	}
	return out, nil
}
