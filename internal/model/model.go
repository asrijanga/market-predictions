package model

import (
	"fmt"
	"math"
	"time"

	"github.com/asrijanga/market-predictions/internal/options"
	"github.com/asrijanga/market-predictions/internal/pack"
	"github.com/asrijanga/market-predictions/internal/quant"
)

// Drift modes.
const (
	// DriftCapped is the default: the fitted trend, but capped at a modest
	// annual return. A stock's six-month trend is a weak predictor of its
	// next three months, and an uncapped trend makes every long call look
	// like a winner, which is how option models lie to their owners.
	DriftCapped      = "capped"
	DriftTrend       = "trend"        // the shrunk fitted trend, uncapped
	DriftRiskNeutral = "risk-neutral" // the stock earns the risk-free rate
	DriftZero        = "zero"         // no expected log drift
)

// DriftGrid is the set of annualised simple returns the sensitivity
// analysis reruns every plan against.
var DriftGrid = []float64{-0.20, -0.10, 0, 0.05, 0.10, 0.20, 0.40}

// Options configures the analysis.
type Options struct {
	Paths     int
	Seed      uint64
	Rate      float64
	MinOI     int64
	MaxSpread float64
	MinDelta  float64
	MaxDelta  float64
	DriftMode string
	DriftCap  float64 // maximum annualised simple return under DriftCapped
	TopN      int
	MinPTrade float64
	MaxExpiry int // furthest expiry to consider, in trading days
	EntryDays int // how far out an entry window may start, in trading days
}

// Defaults returns the standard settings.
func Defaults() Options {
	return Options{
		Paths: 20000, Seed: 1, Rate: 0.04, MinOI: 250, MaxSpread: 0.20,
		MinDelta: 0.25, MaxDelta: 0.70, DriftMode: DriftCapped, DriftCap: 0.12,
		TopN: 5, MinPTrade: 0.25, MaxExpiry: 110, EntryDays: 63,
	}
}

// VolTermRow compares what the option market charges for one expiry with
// what the volatility model forecasts over the same span.
type VolTermRow struct {
	Expiry      time.Time `json:"expiry"`
	Days        int       `json:"days"`
	Monthly     bool      `json:"monthly"`
	MarketIV    float64   `json:"market_iv"`
	ForecastVol float64   `json:"forecast_vol"`
	Premium     float64   `json:"premium"` // market IV minus forecast, in vol points
	EventAhead  bool      `json:"event_ahead"`
	ModelIV     float64   `json:"model_iv"` // the fitted surface's own value, for fit quality
}

// DistRow is the simulated price distribution at one expiry.
type DistRow struct {
	Expiry   time.Time `json:"expiry"`
	Days     int       `json:"days"`
	P5       float64   `json:"p5"`
	P25      float64   `json:"p25"`
	Median   float64   `json:"median"`
	P75      float64   `json:"p75"`
	P95      float64   `json:"p95"`
	ProbUp   float64   `json:"prob_up"`
	Expected float64   `json:"expected"`
}

// Result is the full quantitative verdict.
type Result struct {
	Symbol      string    `json:"symbol"`
	AsOf        time.Time `json:"as_of"`
	Spot        float64   `json:"spot"`
	Paths       int       `json:"paths"`
	Seed        uint64    `json:"seed"`
	DriftMode   string    `json:"drift_mode"`
	DailyDrift  float64   `json:"daily_drift"`
	AnnualDrift float64   `json:"annual_drift"`
	TrendDrift  float64   `json:"trend_drift_annual"` // the uncapped fitted trend, for reference
	DriftGrid   []float64 `json:"drift_grid"`
	Horizon     int       `json:"horizon_days"`
	Rate        float64   `json:"rate"`
	Elapsed     string    `json:"elapsed"`

	GARCH        GARCH      `json:"garch"`
	GARCHVol     float64    `json:"garch_vol_now"` // annualised one-step forecast
	LongRunVol   float64    `json:"long_run_vol"`  // annualised unconditional
	VolScores    []VolScore `json:"vol_model_scores"`
	IV           IVModel    `json:"iv_model"`
	ImpliedMove  float64    `json:"implied_earnings_move"` // the market's earnings-day move
	EarningsIdx  int        `json:"earnings_idx"`
	EarningsDate time.Time  `json:"earnings_date"`

	VolTerm    []VolTermRow `json:"vol_term"`
	Dist       []DistRow    `json:"distribution"`
	Contracts  []Contract   `json:"contracts"`
	BuyNow     []Strategy   `json:"buy_now"`
	Strategies []Strategy   `json:"strategies"`
	Warnings   []string     `json:"warnings,omitempty"`
}

// Analyze runs the whole pipeline: fit the volatility model, decompose the
// implied-volatility surface, simulate forward paths, and search entry
// windows for the highest expected value.
func Analyze(p *pack.Pack, o Options) (*Result, error) {
	closes := p.ModelCloses
	if len(closes) < 120 {
		closes = quant.Closes(p.Bars)
	}
	if len(closes) < 120 {
		return nil, fmt.Errorf("model: need at least 120 closes, have %d", len(closes))
	}
	returns := quant.LogReturns(closes)

	start := time.Now()
	r := &Result{
		Symbol: p.Symbol, AsOf: p.AsOf, Spot: p.Price, Paths: o.Paths, Seed: o.Seed,
		DriftMode: o.DriftMode, Rate: o.Rate, EarningsDate: p.EarningsDate,
	}

	// 1. Volatility dynamics.
	g := FitGARCH(returns)
	nextVar := g.NextVar(returns)
	r.GARCH = g
	r.GARCHVol = math.Sqrt(nextVar * quant.TradingDaysPerYear)
	r.LongRunVol = math.Sqrt(g.UncondVar * quant.TradingDaysPerYear)
	r.VolScores = ScoreVolModels(returns, min(250, len(returns)/3))

	// 2. Event structure and the implied-volatility surface.
	if !p.EarningsDate.IsZero() {
		r.EarningsIdx = quant.TradingDays(p.AsOf, p.EarningsDate)
	}
	horizon := 0
	var terms []IVTerm
	for _, e := range p.Expiries {
		if e.Days > o.MaxExpiry || e.Days < 5 || e.ATMCallIV <= 0 {
			continue
		}
		horizon = max(horizon, e.Days)
		terms = append(terms, IVTerm{
			Days: e.Days, IV: e.ATMCallIV,
			HasEvent: r.EarningsIdx > 0 && r.EarningsIdx <= e.Days,
		})
	}
	if horizon == 0 {
		return nil, fmt.Errorf("model: no usable option expiries for %s", p.Symbol)
	}
	r.Horizon = horizon
	ivm := FitIVModel(terms, r.EarningsIdx)
	for _, e := range p.Expiries {
		if e.Monthly && e.Days > 15 && len(e.Calls) >= 3 {
			ivm.SkewSlope = FitSkew(e.Calls, p.Price)
			break
		}
	}
	r.IV = ivm
	r.ImpliedMove = ivm.JumpStdev
	if !ivm.Fitted {
		r.Warnings = append(r.Warnings, "implied-volatility surface could not be decomposed; event premium assumed zero")
	}
	if r.EarningsIdx == 0 {
		r.Warnings = append(r.Warnings, "no earnings date available; timing ignores event risk")
	}

	// 3. Drift. The trend drift is the pack's benchmark-shrunk trend, read
	// back out of the projection it produced, and capped by default.
	trend := 0.0
	if p.Trend.HorizonDays > 0 {
		trend = math.Log1p(p.Trend.ExpectedReturn) / float64(p.Trend.HorizonDays)
	}
	r.TrendDrift = math.Expm1(trend * quant.TradingDaysPerYear)
	rnDrift := o.Rate/quant.TradingDaysPerYear - 0.5*g.UncondVar
	drift := trend
	switch o.DriftMode {
	case DriftZero:
		drift = 0
	case DriftRiskNeutral:
		drift = rnDrift
	case DriftTrend:
		drift = trend
	default: // DriftCapped
		hi := math.Log1p(o.DriftCap) / quant.TradingDaysPerYear
		lo := math.Log1p(-o.DriftCap) / quant.TradingDaysPerYear
		drift = math.Max(lo, math.Min(hi, trend))
	}
	r.DailyDrift = drift
	r.AnnualDrift = math.Expm1(drift * quant.TradingDaysPerYear)
	r.DriftGrid = DriftGrid

	// 4. Simulate, under the chosen drift and under a risk-neutral drift.
	cfg := SimConfig{
		Paths: o.Paths, Horizon: horizon, DailyDrift: drift, StartVar: nextVar,
		EarningsIdx: r.EarningsIdx, JumpStdev: ivm.JumpStdev, Seed: o.Seed,
	}
	if cfg.EarningsIdx > horizon {
		cfg.EarningsIdx = 0
	}
	sim := Simulate(g, g.Resid, cfg)
	// One simulation per point on the drift grid, so every plan can be
	// re-scored under an assumption the reader chooses instead of ours.
	gridSims := make([]*Sim, len(DriftGrid))
	for i, annual := range DriftGrid {
		gcfg := cfg
		gcfg.DailyDrift = math.Log1p(annual) / quant.TradingDaysPerYear
		gridSims[i] = Simulate(g, g.Resid, gcfg)
	}

	// 5. Volatility term structure and the simulated distribution.
	for _, e := range p.Expiries {
		// Expiries inside a week price a pin, not a view; they distort the
		// term structure and are left out.
		if e.Days > horizon || e.Days < 5 || e.ATMCallIV <= 0 {
			continue
		}
		eventAhead := r.EarningsIdx > 0 && r.EarningsIdx <= e.Days
		row := VolTermRow{
			Expiry: e.Expiry, Days: e.Days, Monthly: e.Monthly, MarketIV: e.ATMCallIV,
			ForecastVol: forecastVol(g, nextVar, e.Days, eventAhead, ivm.JumpStdev),
			EventAhead:  eventAhead, ModelIV: ivm.ATM(e.Days, eventAhead),
		}
		row.Premium = row.MarketIV - row.ForecastVol
		r.VolTerm = append(r.VolTerm, row)
		if !e.Monthly {
			continue
		}
		r.Dist = append(r.Dist, DistRow{
			Expiry: e.Expiry, Days: e.Days,
			P5: p.Price * sim.Quantile(e.Days, 0.05), P25: p.Price * sim.Quantile(e.Days, 0.25),
			Median: p.Price * sim.Quantile(e.Days, 0.50), P75: p.Price * sim.Quantile(e.Days, 0.75),
			P95: p.Price * sim.Quantile(e.Days, 0.95), ProbUp: sim.ProbAbove(e.Days, 1),
			Expected: p.Price * meanRatio(sim, e.Days),
		})
	}

	// 6. Candidate contracts.
	r.Contracts = selectContracts(p, r, ivm, g, nextVar, o)
	if len(r.Contracts) == 0 {
		return nil, fmt.Errorf("model: no liquid call contracts for %s within the filters", p.Symbol)
	}

	// 7. Buy-today baseline and the ranked window search.
	today := Window{StartIdx: 0, EndIdx: 0, Start: p.AsOf, End: p.AsOf, Label: "buy today"}
	immediate := Trigger{Kind: TriggerImmediate}
	for _, c := range r.Contracts {
		sched := ivSchedule(ivm, c, p.Price, horizon)
		stats, returns := EvaluateDetailed(sim, p.Price, c, today, immediate, sched, o.Rate)
		stats.KellyFraction = KellyFraction(returns)
		s := Strategy{Window: today, Trigger: immediate, Contract: c, Stats: stats}
		addSensitivity(&s, gridSims, ivm, p.Price, horizon, o.Rate)
		r.BuyNow = append(r.BuyNow, s)
	}

	entryHorizon := min(horizon-minRunway, o.EntryDays)
	all := Search(sim, p.Price, ivm, r.Contracts, BuildWindows(p.AsOf, entryHorizon, r.EarningsIdx), DefaultTriggers(), o.Rate)
	top := TopDistinct(all, o.TopN, o.MinPTrade)
	for i := range top {
		sched := ivSchedule(ivm, top[i].Contract, p.Price, horizon)
		_, returns := EvaluateDetailed(sim, p.Price, top[i].Contract, top[i].Window, top[i].Trigger, sched, o.Rate)
		top[i].Stats.KellyFraction = KellyFraction(returns)
		addSensitivity(&top[i], gridSims, ivm, p.Price, horizon, o.Rate)
	}
	r.Strategies = top
	r.Elapsed = time.Since(start).Round(time.Millisecond).String()
	return r, nil
}

// addSensitivity re-scores a plan under every drift on the grid and solves
// for the drift at which it breaks even, by linear interpolation between
// the bracketing grid points.
func addSensitivity(s *Strategy, sims []*Sim, ivm IVModel, spot float64, horizon int, rate float64) {
	sched := ivSchedule(ivm, s.Contract, spot, horizon)
	s.DriftCurve = make([]float64, len(sims))
	for i, sim := range sims {
		s.DriftCurve[i] = Evaluate(sim, spot, s.Contract, s.Window, s.Trigger, sched, rate).MeanReturn
	}
	s.BreakEvenDrift = BreakEven(math.Inf(1))
	for i := 1; i < len(s.DriftCurve); i++ {
		lo, hi := s.DriftCurve[i-1], s.DriftCurve[i]
		if lo <= 0 && hi > 0 && hi != lo {
			t := -lo / (hi - lo)
			s.BreakEvenDrift = BreakEven(DriftGrid[i-1] + t*(DriftGrid[i]-DriftGrid[i-1]))
			return
		}
	}
	if len(s.DriftCurve) > 0 && s.DriftCurve[0] > 0 {
		s.BreakEvenDrift = BreakEven(math.Inf(-1))
	}
}

// forecastVol is the model's expected volatility over an option's life: the
// GARCH diffusive forecast, with the earnings jump added when the event
// falls before expiry, so it is comparable with the market's implied number.
func forecastVol(g GARCH, nextVar float64, days int, eventAhead bool, jump float64) float64 {
	v := g.AvgVol(nextVar, days)
	if !eventAhead || jump <= 0 {
		return v
	}
	years := float64(days) / quant.TradingDaysPerYear
	return math.Sqrt((v*v*years + jump*jump) / years)
}

func meanRatio(s *Sim, day int) float64 {
	var sum float64
	for p := 0; p < s.Paths; p++ {
		sum += s.At(p, day)
	}
	return sum / float64(s.Paths)
}

// selectContracts keeps liquid calls in the requested delta band on monthly
// expiries, at most four per expiry.
func selectContracts(p *pack.Pack, r *Result, ivm IVModel, g GARCH, nextVar float64, o Options) []Contract {
	var out []Contract
	for _, e := range p.Expiries {
		if !e.Monthly || e.Days < minRunway+5 || e.Days > o.MaxExpiry {
			continue
		}
		n := 0
		for _, c := range e.Calls {
			if n >= 4 || c.Mid <= 0 || c.OpenInterest < o.MinOI || c.SpreadPct > o.MaxSpread {
				continue
			}
			if c.Delta < o.MinDelta || c.Delta > o.MaxDelta {
				continue
			}
			eventAhead := r.EarningsIdx > 0 && r.EarningsIdx <= e.Days
			modelIV := forecastVol(g, nextVar, e.Days, eventAhead, ivm.JumpStdev)
			years := float64(e.Days) / quant.TradingDaysPerYear
			out = append(out, Contract{
				Expiry: e.Expiry, ExpiryIdx: e.Days, Strike: c.Strike,
				MarketMid: c.Mid, SpreadPct: c.SpreadPct, OpenInterest: c.OpenInterest,
				MarketIV: c.IV, ModelIV: modelIV,
				ModelPrice: options.CallPrice(p.Price, c.Strike, years, o.Rate, modelIV),
				VolEdge:    modelIV - c.IV, Delta: c.Delta, Breakeven: c.Breakeven,
			})
			n++
		}
	}
	return out
}
