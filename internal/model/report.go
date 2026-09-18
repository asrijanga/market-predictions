package model

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Render writes the short answer: which way the stock leans, why, where it
// is likely to be in three months and in a year, and the one trade that
// follows. Everything that supports it is in RenderDetail.
func Render(r *Result) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	d := func(t time.Time) string { return t.Format("2006-01-02") }

	w("# %s is %s", r.Symbol, strings.ToUpper(r.Stance))
	w("")
	w("As of %s, spot %.2f. Composite signal score %+.2f on a scale of -1 to +1, from %d measurements of trend, relative strength, volatility regime and option-market positioning.",
		d(r.AsOf), r.Spot, r.Score, len(r.Signals))

	w("")
	w("## Direction")
	w("")
	w("| Horizon | Date | Call | Probability up | Typical move | Average move | 80%% range |")
	w("|---|---|---|---|---|---|---|")
	for _, o := range r.Outlooks {
		w("| %s | %s | **%s** | %.0f%% | %s | %s | %.0f to %.0f |",
			o.Name, d(o.Date), strings.ToUpper(o.Direction), 100*o.ProbUp,
			pct(o.MedianReturn), pct(o.MeanReturn), o.Low, o.High)
	}
	w("")
	w("Typical is the median path and average is the mean. The mean is the larger of the two because prices compound: a stock can double but cannot fall more than all the way, so a few large gains pull the average above the outcome you should actually expect.")
	w("")
	if len(r.Outlooks) > 0 {
		o := r.Outlooks[0]
		w("The probability of being up is %.0f%% over the quarter against %.0f%% if %s earned only the risk-free rate. The gap is what the signals are adding; the rest is the shape of the distribution.",
			100*o.ProbUp, 100*o.ProbUpBaseline, r.Symbol)
	}

	bull, bear := splitSignals(r.Signals)
	w("")
	w("## Why %s", r.Stance)
	w("")
	if len(bull) == 0 {
		w("Nothing in the evidence points up.")
	}
	for _, s := range bull {
		w("- **%s**: %s. %s", s.Name, s.Detail, contributionText(s))
	}
	w("")
	w("## What argues against it")
	w("")
	if len(bear) == 0 {
		w("Nothing in the evidence points the other way, which is itself a warning: a one-sided read is usually an incomplete one.")
	}
	for _, s := range bear {
		w("- **%s**: %s. %s", s.Name, s.Detail, contributionText(s))
	}

	w("")
	w("## What it looks worth")
	w("")
	v := r.Valuation
	if r.FairValue <= 0 {
		w("Not enough of what this company reports is published to value it on its earnings.")
	} else {
		w("**Fair value %s** against a price of %s, a gap of %s.",
			dollars(r.FairValue), dollars(r.Spot), pct(r.Upside))
		w("")
		w("| Model | Value | Weight | What it reads |")
		w("|---|---|---|---|")
		for _, e := range v.Estimates {
			w("| %s | %s | %.0f%% | %s |", capitalise(e.Method), dollars(e.Value), 100*e.Weight, e.Note)
		}
		w("")
		w("- Discounted at %.1f%%, being the risk-free rate plus this company's share of market risk, and growing at %.1f%% a year.",
			100*v.DiscountRate, 100*v.Growth)
		if v.TrailingPE > 0 {
			w("- Trading on %.1f times trailing earnings and %.1f times next year's.", v.TrailingPE, v.ForwardPE)
		}
		w("- The models agree %s: they span %.0f%% of the blended figure.", v.Confidence, 100*v.Spread)
	}

	w("")
	w("## How long it might take")
	w("")
	if rev := r.Reversion; rev.Fitted && rev.MedianMonths > 0 {
		w("This multiple has returned to its own level with a half-life of %s, and from a gap this size half of simulated paths reach fair value within %s.",
			months(rev.HalfLife), months(rev.MedianMonths))
		w("")
		w("- Half the gap closes in %s on the fitted speed, and %.0f%% of paths arrive inside a year.",
			months(rev.HalfLife), 100*rev.WithinYear)
		w("")
	} else {
		w("This multiple has not returned to its own level reliably enough to time, so no estimate is offered. A gap can stay open for years.")
	}

	w("## How much to trust this")
	w("")
	w("- The direction call is a weighted score of observable evidence. It sets the drift of the simulation, and drift is what dominates option returns, so treat the %+.1f%%/yr it implies as the assumption to argue with.", 100*r.AnnualDrift)
	w("- The one-year range is wide by construction: %s. Volatility compounds with the square root of time and a year contains four earnings reports.",
		yearRangeText(r))
	w("- %d simulated paths, seed %d.", r.Paths, r.Seed)
	w("- This is an experiment, built for fun. It is not financial advice and must not be used for any financial benefit.")
	if len(r.Warnings) > 0 {
		w("- Data warnings: %s.", strings.Join(r.Warnings, "; "))
	}
	w("")
	w("Run with -detail for the volatility model, the implied-volatility surface and the simulated distribution.")
	return b.String()
}

// splitSignals divides the evidence into what supports the call and what
// works against it, keeping the strongest few of each.
func splitSignals(sigs []Signal) (bull, bear []Signal) {
	for _, s := range sigs {
		if s.Contribution > 0 {
			bull = append(bull, s)
		} else if s.Contribution < 0 {
			bear = append(bear, s)
		}
	}
	return trim(bull, 5), trim(bear, 5)
}

func trim(s []Signal, n int) []Signal {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func contributionText(s Signal) string {
	strength := "weakly"
	switch a := math.Abs(s.Score); {
	case a >= 0.75:
		strength = "strongly"
	case a >= 0.35:
		strength = "moderately"
	}
	side := "bullish"
	if s.Contribution < 0 {
		side = "bearish"
	}
	return fmt.Sprintf("Scores %+.2f, %s %s, weight %.0f%%.", s.Score, strength, side, 100*s.Weight)
}

func yearRangeText(r *Result) string {
	for _, o := range r.Outlooks {
		if o.Days > 200 {
			return fmt.Sprintf("%.0f to %.0f covers eight cases in ten", o.Low, o.High)
		}
	}
	return "no one-year horizon was simulated"
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// RenderDetail writes the model evidence behind the verdict: the volatility
// fit, the implied-volatility surface, the simulated distribution, the
// contract screen and every ranked plan.
func RenderDetail(r *Result) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	d := func(t time.Time) string { return t.Format("2006-01-02") }

	w("# %s model detail", r.Symbol)
	w("")
	w("As of %s, spot %.2f. %d simulated paths (seed %d), option horizon %d trading days, drift mode %q, risk-free %.1f%%. Computed in %s.",
		d(r.AsOf), r.Spot, r.Paths, r.Seed, r.Horizon, r.DriftMode, 100*r.Rate, r.Elapsed)
	if len(r.Warnings) > 0 {
		w("")
		w("Warnings: %s", strings.Join(r.Warnings, "; "))
	}

	w("")
	w("## Signal detail")
	w("")
	w("| Signal | Reading | Score | Weight | Contribution |")
	w("|---|---|---|---|---|")
	for _, s := range r.Signals {
		w("| %s | %s | %+.2f | %.0f%% | %+.3f |", s.Name, s.Detail, s.Score, 100*s.Weight, s.Contribution)
	}
	w("")
	w("Composite %+.2f, which reads as %s and implies a drift of %+.1f%%/yr.", r.Score, r.Stance, 100*r.AnnualDrift)
	w("")
	w("## Volatility model")
	w("")
	g := r.GARCH
	w("GARCH(1,1) fitted by maximum likelihood with variance targeting: alpha %.3f, beta %.3f, persistence %.3f, shock half-life %.0f trading days.", g.Alpha, g.Beta, g.Persistence(), g.HalfLife())
	w("Current conditional volatility %.0f%% annualised against a long-run level of %.0f%%.", 100*r.GARCHVol, 100*r.LongRunVol)
	if len(r.VolScores) > 0 {
		w("")
		w("Walk-forward one-day-ahead forecast accuracy (QLIKE loss, lower is better; the constant-variance row is the do-nothing benchmark):")
		w("")
		w("| Model | QLIKE | RMSE (daily variance) |")
		w("|---|---|---|")
		best := r.VolScores[0]
		var constant float64
		for _, s := range r.VolScores {
			w("| %s | %.4f | %.2e |", s.Name, s.QLIKE, s.RMSE)
			if s.QLIKE < best.QLIKE {
				best = s
			}
			if s.Name == "Constant variance" {
				constant = s.QLIKE
			}
		}
		w("")
		if best.Name == "Constant variance" {
			w("On this sample no conditional model beats a constant variance, so the volatility forecast below is close to a flat one and should be read that way.")
		} else {
			w("Best on this sample: %s, improving on a constant variance by %.1f%%.", best.Name, 100*(constant-best.QLIKE)/constant)
		}
	}

	w("")
	w("## Implied volatility surface")
	w("")
	if r.IV.Fitted {
		w("Decomposing listed implied variance into a diffusive part that accrues with time and a one-off earnings bump gives a base volatility of **%.0f%%** and an implied earnings-day move of **±%.1f%%**.", 100*r.IV.BaseVol, 100*r.ImpliedMove)
		if r.EarningsIdx > 0 {
			w("")
			w("That bump is what disappears when the report passes. For an option with a month left at that moment, the model expects implied volatility to fall by about **%.0f%%** of its level overnight.", 100*r.IV.CrushPct(21))
		}
	} else {
		w("The surface could not be decomposed into diffusive and event variance, so no event premium is assumed.")
	}
	w("Skew across strikes: %.2f volatility points per unit of log-moneyness.", r.IV.SkewSlope)
	w("")
	w("| Expiry | Days | Market IV | Model forecast | Premium | Earnings ahead |")
	w("|---|---|---|---|---|---|")
	for _, v := range r.VolTerm {
		w("| %s%s | %d | %.0f%% | %.0f%% | %+.1f pts | %s |", d(v.Expiry), monthly(v.Monthly), v.Days, 100*v.MarketIV, 100*v.ForecastVol, 100*v.Premium, yesNo(v.EventAhead))
	}
	w("")
	w("A positive premium means the option market is charging more volatility than the model forecasts, which is the usual state of affairs and the cost of being long options.")

	w("")
	w("## Simulated price distribution")
	w("")
	w("Filtered historical simulation: GARCH supplies the variance dynamics, the stock's own standardized residuals supply the shape of each shock, and the earnings day carries an extra draw scaled to the market-implied move.")
	w("")
	w("Drift is %+.1f%%/yr under the %q setting. The fitted six-month trend would imply %+.1f%%/yr, which is capped because a six-month trend is a weak predictor of the next three months and an uncapped one flatters every long call.", 100*r.AnnualDrift, r.DriftMode, 100*r.TrendDrift)
	w("")
	w("| Expiry | Days | 5%% | 25%% | Median | Mean | 75%% | 95%% | P(above spot) |")
	w("|---|---|---|---|---|---|---|---|---|")
	for _, x := range r.Dist {
		w("| %s | %d | %.0f | %.0f | %.0f | %.0f | %.0f | %.0f | %.0f%% |", d(x.Expiry), x.Days, x.P5, x.P25, x.Median, x.Expected, x.P75, x.P95, 100*x.ProbUp)
	}

	w("")
	w("## Method and limitations")
	w("")
	w("- Volatility: GARCH(1,1), variance targeting, Gaussian likelihood, fitted on %d daily log returns.", r.GARCH.n())
	w("- Event structure: implied variance is split into a diffusive term and one earnings bump by non-negative least squares across listed expiries. The earnings date is an estimate until the company confirms it.")
	w("- Paths: filtered historical simulation. Volatility clusters as the GARCH process dictates; shocks are drawn from the stock's own standardized residuals, so skew and fat tails survive; the earnings day carries an independent normal jump.")
	w("- Pricing at entry: Black-Scholes on the fitted surface, which is held sticky in strike space, plus half the current bid-ask spread as slippage. The surface does not respond to the simulated path, so a plan that only pays off through a volatility spike is not being credited for one.")
	w("- Payoff: European, held to expiry, no early exercise and no dividends.")
	w("- Drift is the weakest input: a capped, shrunk trend estimate, not a forecast of news. The break-even drift and the sensitivity table are there so the reader can substitute their own view.")
	w("- Trading-day counts ignore exchange holidays.")
	w("- This is an experiment, built for fun. It is not financial advice and must not be used for any financial benefit.")
	return b.String()
}

// n is the estimation sample size.
func (g GARCH) n() int { return len(g.Resid) }

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// monthly marks the standard monthly expiries, which carry the liquidity.
func monthly(b bool) string {
	if b {
		return " (monthly)"
	}
	return ""
}

// pct renders a fraction as a signed percentage.
func pct(x float64) string { return fmt.Sprintf("%+.1f%%", 100*x) }

// dollars renders a share price the way a quote does.
func dollars(v float64) string { return fmt.Sprintf("$%.2f", v) }

// months renders a duration in whole months, or in years once quoting
// months would be false precision.
func months(m float64) string {
	switch {
	case m <= 0:
		return "no estimate"
	case m < 1.5:
		return "about a month"
	case m < 24:
		return fmt.Sprintf("%.0f months", m)
	default:
		return fmt.Sprintf("%.1f years", m/12)
	}
}
