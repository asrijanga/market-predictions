package model

import (
	"fmt"
	"math"
	"time"

	"github.com/asrijanga/market-predictions/internal/pack"
	"github.com/asrijanga/market-predictions/internal/quant"
	"github.com/asrijanga/market-predictions/internal/valuation"
)

// ReversionHorizonYears bounds the timing simulation. Beyond five years a
// valuation gap says more about the forecast than about the price, and a
// number quoted from out there would be false precision.
const ReversionHorizonYears = 5

// Version identifies the modelling pipeline. Anything that changes the
// answer for the same inputs - a new signal, a different weight, a change
// to the simulation or the ranking - must bump it, because cached results
// are keyed on it.
const Version = "2026-09-18.1-fair-value"

// Drift modes.
const (
	// DriftCapped is the default: the fitted trend, but capped at a modest
	// annual return. A stock's six-month trend is a weak predictor of its
	// next three months, and an uncapped trend makes every long call look
	// like a winner, which is how option models lie to their owners.
	// DriftSignal is the default: an expected return implied by the
	// composite of the directional signals, bounded by an equity risk
	// premium either side of the risk-free rate.
	DriftSignal      = "signal"
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
	Paths       int
	Seed        uint64
	Rate        float64
	MinOI       int64
	MaxSpread   float64
	MinDelta    float64
	MaxDelta    float64
	DriftMode   string
	DriftCap    float64 // maximum annualised simple return under DriftCapped
	PremiumSpan float64 // how far the signal score may move drift from the risk-free rate
	OutlookDays int     // horizon of the directional forecast, in trading days
	TopN        int
	MinPTrade   float64
	MaxExpiry   int // furthest expiry to consider, in trading days
	EntryDays   int // how far out an entry window may start, in trading days

	// Progress, when set, is called as each stage of the analysis begins,
	// so a caller can drive a progress display. Fraction runs 0 to 1.
	Progress func(stage string, fraction float64)
}

// Defaults returns the standard settings.
func Defaults() Options {
	return Options{
		Paths: 20000, Seed: 1, Rate: 0.04, MinOI: 250, MaxSpread: 0.20,
		MinDelta: 0.25, MaxDelta: 0.70, DriftMode: DriftSignal, DriftCap: 0.12,
		PremiumSpan: 0.15, OutlookDays: 252,
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

	Stance   string    `json:"stance"`
	Score    float64   `json:"score"`
	Signals  []Signal  `json:"signals"`
	Outlooks []Outlook `json:"outlooks"`
	Horizon  int       `json:"horizon_days"`
	Rate     float64   `json:"rate"`
	Elapsed  string    `json:"elapsed"`

	GARCH        GARCH      `json:"garch"`
	GARCHVol     float64    `json:"garch_vol_now"` // annualised one-step forecast
	LongRunVol   float64    `json:"long_run_vol"`  // annualised unconditional
	VolScores    []VolScore `json:"vol_model_scores"`
	IV           IVModel    `json:"iv_model"`
	ImpliedMove  float64    `json:"implied_earnings_move"` // the market's earnings-day move
	EarningsIdx  int        `json:"earnings_idx"`
	EarningsDate time.Time  `json:"earnings_date"`

	VolTerm []VolTermRow `json:"vol_term"`
	Dist    []DistRow    `json:"distribution"`

	// Valuation is what the company is worth on what it reports, and
	// Reversion is how long the market has taken to agree.
	Valuation valuation.Result    `json:"valuation"`
	Reversion valuation.Reversion `json:"reversion"`
	FairValue float64             `json:"fair_value"`
	Upside    float64             `json:"upside"`
	Warnings  []string            `json:"warnings,omitempty"`
}

// Analyze runs the whole pipeline: fit the volatility model, decompose the
// implied-volatility surface, simulate forward paths, value the company on
// what it reports, and estimate how long the market has historically taken
// to close a gap of the size this one shows.
func Analyze(p *pack.Pack, o Options) (*Result, error) {
	closes := p.ModelCloses
	if len(closes) < 120 {
		closes = quant.Closes(p.Bars)
	}
	if len(closes) < 120 {
		return nil, fmt.Errorf("model: need at least 120 closes, have %d", len(closes))
	}
	returns := quant.LogReturns(closes)

	report := o.Progress
	if report == nil {
		report = func(string, float64) {}
	}
	start := time.Now()
	r := &Result{
		Symbol: p.Symbol, AsOf: p.AsOf, Spot: p.Price, Paths: o.Paths, Seed: o.Seed,
		DriftMode: o.DriftMode, Rate: o.Rate, EarningsDate: p.EarningsDate,
	}

	// 1. Volatility dynamics.
	report("fitting GARCH(1,1) volatility model", 0.05)
	g := FitGARCH(returns)
	nextVar := g.NextVar(returns)
	r.GARCH = g
	r.GARCHVol = math.Sqrt(nextVar * quant.TradingDaysPerYear)
	r.LongRunVol = math.Sqrt(g.UncondVar * quant.TradingDaysPerYear)
	report("scoring volatility forecasts out of sample", 0.15)
	r.VolScores = ScoreVolModels(returns, min(250, len(returns)/3))

	// 2. Event structure and the implied-volatility surface.
	report("decomposing the implied volatility surface", 0.35)
	if !p.EarningsDate.IsZero() {
		r.EarningsIdx = quant.TradingDays(p.AsOf, p.EarningsDate)
	}
	// The horizon has to cover every expiry a contract can be drawn from,
	// which is a wider set than the expiries that can be fitted: a monthly
	// expiry with liquid strikes but no solvable at-the-money volatility
	// yields contracts while contributing no term to the fit. Letting the
	// two diverge leaves contracts expiring past the end of the simulations
	// built to that horizon.
	horizon := 0
	var terms []IVTerm
	for _, e := range p.Expiries {
		if e.Days > o.MaxExpiry || e.Days < 5 {
			continue
		}
		horizon = max(horizon, e.Days)
		if e.ATMCallIV <= 0 {
			continue
		}
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

	// 3. What the company is worth on what it reports. This runs before
	// the direction is scored, because how far the price sits from fair
	// value is one of the things that score is made of.
	report("valuing the company on its filings", 0.9)
	vin := valuation.Defaults(o.Rate)
	vin.Price, vin.Beta = p.Price, p.Beta
	r.Valuation = valuation.Value(p.Fundamentals, vin, p.PEHistory)
	r.FairValue, r.Upside = r.Valuation.FairValue, r.Valuation.Upside
	r.Warnings = append(r.Warnings, r.Valuation.Warnings...)

	// 4. Direction. The signals are scored next, because the composite is
	// what sets the drift the simulation runs at.
	report("scoring directional signals", 0.45)
	r.Signals, r.Score = Signals(p, ivm, r.Valuation)
	r.Stance = Stance(r.Score)

	// 4. Drift. The trend drift is the pack's benchmark-shrunk trend, read
	// back out of the projection it produced.
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
	case DriftCapped:
		hi := math.Log1p(o.DriftCap) / quant.TradingDaysPerYear
		lo := math.Log1p(-o.DriftCap) / quant.TradingDaysPerYear
		drift = math.Max(lo, math.Min(hi, trend))
	default: // DriftSignal
		drift = math.Log1p(SignalDrift(r.Score, o.Rate, o.PremiumSpan)) / quant.TradingDaysPerYear
	}
	r.DailyDrift = drift
	r.AnnualDrift = math.Expm1(drift * quant.TradingDaysPerYear)
	r.DriftGrid = DriftGrid

	// 5. Simulate. One long run covers both the option plans and the
	// directional forecast; earnings recur roughly quarterly over a year.
	simHorizon := max(horizon, o.OutlookDays)
	cfg := SimConfig{
		Paths: o.Paths, Horizon: simHorizon, DailyDrift: drift, StartVar: nextVar,
		EarningsDays: earningsSchedule(r.EarningsIdx, simHorizon), JumpStdev: ivm.JumpStdev, Seed: o.Seed,
	}
	report(fmt.Sprintf("simulating %d price paths", o.Paths), 0.5)
	sim := Simulate(g, g.Resid, cfg)

	rnCfg := cfg
	rnCfg.DailyDrift = o.Rate/quant.TradingDaysPerYear - 0.5*g.UncondVar
	rnSim := Simulate(g, g.Resid, rnCfg)

	// The drift grid only has to reach the furthest expiry, since it exists
	// to re-score option plans rather than the forecast.
	report("running the drift sensitivity grid", 0.7)
	gridSims := make([]*Sim, len(DriftGrid))
	for i, annual := range DriftGrid {
		gcfg := cfg
		gcfg.Horizon = horizon
		gcfg.EarningsDays = earningsSchedule(r.EarningsIdx, horizon)
		gcfg.DailyDrift = math.Log1p(annual) / quant.TradingDaysPerYear
		gridSims[i] = Simulate(g, g.Resid, gcfg)
	}

	// 6. Directional forecast.
	report("building the quarter and year outlook", 0.8)
	quarter := min(63, simHorizon)
	r.Outlooks = append(r.Outlooks, BuildOutlook("Next quarter", quarter, p.AsOf, p.Price, sim, rnSim))
	if o.OutlookDays > quarter {
		r.Outlooks = append(r.Outlooks, BuildOutlook("Next year", min(o.OutlookDays, simHorizon), p.AsOf, p.Price, sim, rnSim))
	}

	// 7. Volatility term structure and the option-chain distribution.
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

	// 8. How long the market has taken to close a gap like this one.
	//
	// The gap is what reverts, not the price, so the speed is fitted to the
	// multiple's own history and the simulation runs on the distance to
	// fair value rather than on the level of the shares.
	if r.FairValue > 0 && p.Price > 0 {
		report("estimating how long the gap takes to close", 0.95)
		kappa, sigma, ok := valuation.FitReversion(p.PEHistory, quant.TradingDaysPerYear)
		if !ok {
			// Without a usable fit there is no half-life to quote, and
			// inventing one would be the least honest thing here.
			r.Warnings = append(r.Warnings,
				"this multiple has not reverted reliably, so no timing is offered")
		} else {
			gap := math.Log(p.Price / r.FairValue)
			r.Reversion = valuation.TimeToValue(gap, kappa, sigma, ReversionHorizonYears,
				min(o.Paths, 8000), o.Seed)
		}
	}

	r.Elapsed = time.Since(start).Round(time.Millisecond).String()
	report("done", 1)
	return r, nil
}

// earningsSchedule returns the day indices carrying an earnings jump: the
// next report, then one about every quarter after it for as long as the
// simulation runs.
func earningsSchedule(next, horizon int) []int {
	if next <= 0 {
		return nil
	}
	var out []int
	for d := next; d <= horizon; d += 63 {
		out = append(out, d)
	}
	return out
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
