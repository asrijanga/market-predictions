package model

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/asrijanga/market-predictions/internal/pack"
	"github.com/asrijanga/market-predictions/internal/quant"
)

// Stances, ordered from most bearish to most bullish.
const (
	StanceStrongBear = "strongly bearish"
	StanceBear       = "bearish"
	StanceNeutral    = "neutral"
	StanceBull       = "bullish"
	StanceStrongBull = "strongly bullish"
)

// Signal is one piece of evidence about direction. Score runs from -1
// (maximally bearish) to +1 (maximally bullish) and Contribution is that
// score times the signal's weight in the composite.
type Signal struct {
	Name         string  `json:"name"`
	Detail       string  `json:"detail"`
	Score        float64 `json:"score"`
	Weight       float64 `json:"weight"`
	Contribution float64 `json:"contribution"`
}

// Bullish reports which side of neutral a signal falls on.
func (s Signal) Bullish() bool { return s.Contribution > 0 }

// Signals scores the evidence in a pack and returns the signals sorted by
// how much they move the composite, plus the composite score itself.
//
// Every signal is an observable: a trend, a position against a moving
// average, a relative return, a volatility regime, or what the option
// market's own pricing reveals about demand for downside. None of them are
// derived from the simulation, so the composite can be used to set the
// simulation's drift without reasoning in a circle.
func Signals(p *pack.Pack, ivm IVModel) ([]Signal, float64) {
	trendAnnual := math.Expm1(p.Trend.Drift * quant.TradingDaysPerYear)
	// A trend counts for more when the fit is clean, so R² scales the weight
	// rather than the score.
	trendQuality := 0.4 + 0.6*clamp01(p.Trend.R2)

	var sigs []Signal
	add := func(name, detail string, score, weight float64) {
		score = clamp(score, -1, 1)
		sigs = append(sigs, Signal{name, detail, score, weight, score * weight})
	}

	add("Fitted trend",
		fmt.Sprintf("%+.0f%%/yr over six months, R² %.2f", 100*trendAnnual, p.Trend.R2),
		trendAnnual/0.40, 0.20*trendQuality)

	if p.Year.SMA200 > 0 {
		gap := p.Price/p.Year.SMA200 - 1
		add("Position against the 200-day average",
			fmt.Sprintf("%+.1f%% away from it", 100*gap), gap/0.15, 0.15)
	}

	add("Position against the 50-day average", aboveBelowText(p.Trend.AboveSMA50),
		boolScore(p.Trend.AboveSMA50), 0.10)

	add("Relative strength against the benchmark",
		fmt.Sprintf("%+.1f%% excess return over six months", 100*p.Trend.ExcessReturn6M),
		p.Trend.ExcessReturn6M/0.20, 0.15)

	add("One-month momentum", fmt.Sprintf("%+.1f%%", 100*p.Trend.Return1M),
		p.Trend.Return1M/0.10, 0.10)

	add("Distance from the 52-week high", fmt.Sprintf("%+.1f%%", 100*p.Year.FromHighPct),
		(p.Year.FromHighPct+0.10)/0.10, 0.10)

	// An extended RSI is a mild warning, not a trend signal, so it carries
	// the opposite sign to momentum and a small weight.
	add("RSI(14)", fmt.Sprintf("%.0f", p.Trend.RSI), -(p.Trend.RSI-50)/30, 0.05)

	add("Volatility regime",
		fmt.Sprintf("20-day realised volatility at the %.0fth percentile of the year", 100*p.Year.RealizedVolPctl),
		1-2*p.Year.RealizedVolPctl, 0.05)

	if ivm.SkewSlope != 0 {
		add("Option skew",
			fmt.Sprintf("%.2f volatility points per unit of log-moneyness", ivm.SkewSlope),
			(ivm.SkewSlope+0.25)/0.25, 0.05)
	}

	if ratio := putCallRatio(p); ratio > 0 {
		add("Put/call open interest", fmt.Sprintf("%.2f", ratio), (1-ratio)/0.5, 0.05)
	}

	var score, weights float64
	for _, s := range sigs {
		score += s.Contribution
		weights += s.Weight
	}
	if weights > 0 {
		score /= weights
	}
	sort.SliceStable(sigs, func(i, j int) bool {
		return math.Abs(sigs[i].Contribution) > math.Abs(sigs[j].Contribution)
	})
	return sigs, clamp(score, -1, 1)
}

// Stance turns the composite score into a verdict.
func Stance(score float64) string {
	switch {
	case score >= 0.45:
		return StanceStrongBull
	case score >= 0.15:
		return StanceBull
	case score <= -0.45:
		return StanceStrongBear
	case score <= -0.15:
		return StanceBear
	default:
		return StanceNeutral
	}
}

// SignalDrift maps a composite score to an expected annual return: the
// risk-free rate plus the score's share of an equity risk premium. It is
// bounded by construction, which keeps a strong trend from turning into an
// extrapolated forecast that flatters every long position.
func SignalDrift(score, riskFree, premiumSpan float64) float64 {
	return riskFree + clamp(score, -1, 1)*premiumSpan
}

// Outlook is the simulated distribution at one forecast horizon.
type Outlook struct {
	Name           string    `json:"name"`
	Days           int       `json:"days"`
	Date           time.Time `json:"date"`
	Direction      string    `json:"direction"` // up or down
	ProbUp         float64   `json:"prob_up"`
	ProbUpBaseline float64   `json:"prob_up_risk_neutral"`
	MedianReturn   float64   `json:"median_return"`
	MeanReturn     float64   `json:"mean_return"`
	Median         float64   `json:"median_price"`
	Low            float64   `json:"low_price"`  // 10th percentile
	High           float64   `json:"high_price"` // 90th percentile
}

// BuildOutlook summarises one horizon from the simulated paths, with the
// risk-neutral run alongside it so the reader can see how much of the
// probability comes from the assumed drift rather than from the dispersion.
func BuildOutlook(name string, days int, now time.Time, spot float64, sim, baseline *Sim) Outlook {
	o := Outlook{Name: name, Days: days, Date: TradingDayDate(now, days)}
	if sim == nil || days > sim.Horizon {
		return o
	}
	o.ProbUp = sim.ProbAbove(days, 1)
	o.MedianReturn = sim.Quantile(days, 0.5) - 1
	o.MeanReturn = meanRatio(sim, days) - 1
	o.Median = spot * (1 + o.MedianReturn)
	o.Low = spot * sim.Quantile(days, 0.10)
	o.High = spot * sim.Quantile(days, 0.90)
	o.Direction = "down"
	if o.MedianReturn > 0 {
		o.Direction = "up"
	}
	if baseline != nil && days <= baseline.Horizon {
		o.ProbUpBaseline = baseline.ProbAbove(days, 1)
	}
	return o
}

// putCallRatio is the open-interest-weighted put/call ratio across the
// monthly expiries that a three-month view would actually trade.
func putCallRatio(p *pack.Pack) float64 {
	var calls, puts int64
	for _, e := range p.Expiries {
		if e.Monthly && e.Days >= 15 && e.Days <= 120 {
			calls += e.CallOI
			puts += e.PutOI
		}
	}
	if calls == 0 {
		return 0
	}
	return float64(puts) / float64(calls)
}

func boolScore(b bool) float64 {
	if b {
		return 1
	}
	return -1
}

func aboveBelowText(above bool) string {
	if above {
		return "trading above it"
	}
	return "trading below it"
}

func clamp(x, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, x)) }
func clamp01(x float64) float64       { return clamp(x, 0, 1) }
