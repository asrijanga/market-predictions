package model

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Render writes the quantitative verdict as Markdown.
func Render(r *Result) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	d := func(t time.Time) string { return t.Format("2006-01-02") }
	pct := func(x float64) string { return fmt.Sprintf("%+.1f%%", 100*x) }

	w("# %s call-timing model", r.Symbol)
	w("")
	w("As of %s, spot %.2f. %d simulated paths (seed %d), horizon %d trading days, drift mode %q, risk-free %.1f%%. Computed in %s.",
		d(r.AsOf), r.Spot, r.Paths, r.Seed, r.Horizon, r.DriftMode, 100*r.Rate, r.Elapsed)
	if len(r.Warnings) > 0 {
		w("")
		w("Warnings: %s", strings.Join(r.Warnings, "; "))
	}

	w("")
	w("## Verdict")
	w("")
	if len(r.Strategies) == 0 {
		w("No entry plan clears the filters: nothing fills often enough to be worth naming.")
	} else {
		best := r.Strategies[0]
		w("Best plan by expected log growth: **%s** between **%s and %s**, buying the **%s %g call**.",
			triggerText(best.Trigger, r.Spot), d(best.Window.Start), d(best.Window.End), d(best.Contract.Expiry), best.Contract.Strike)
		w("")
		w("It fills on %.0f%% of paths. When it fills the expected return is %s, the median outcome is %s, and there is a %.0f%% chance of finishing above the entry cost. Buying the same contract today returns %s, so waiting for this trigger is worth %s.",
			100*best.Stats.PTrade, pct(best.Stats.MeanReturn), pct(best.Stats.MedianReturn), 100*best.Stats.PProfit,
			pct(buyNowReturn(r, best.Contract)), pct(best.Stats.MeanReturn-buyNowReturn(r, best.Contract)))
		w("")
		if best.Stats.MeanReturn <= 0 {
			w("Note that the expected return is negative under the default drift of %+.1f%%/yr. The plan breaks even only if %s compounds at %s. Treat the ranking as relative value, not as a reason to be long.",
				100*r.AnnualDrift, r.Symbol, breakEvenText(best.BreakEvenDrift))
		} else {
			w("The plan breaks even if %s compounds at %s, against the %+.1f%%/yr the simulation assumes and the %+.1f%%/yr its fitted six-month trend implies.",
				r.Symbol, breakEvenText(best.BreakEvenDrift), 100*r.AnnualDrift, 100*r.TrendDrift)
		}
	}

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
		w("| %s%s | %d | %.0f%% | %.0f%% | %+.1f pts | %s |", d(v.Expiry), monthlyMark(v.Monthly), v.Days, 100*v.MarketIV, 100*v.ForecastVol, 100*v.Premium, yesNo(v.EventAhead))
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
	w("## Contract screen (buying today)")
	w("")
	w("| Expiry | Strike | Delta | Mid | Market IV | Forecast vol | Vol edge | Model price | Expected return | P(profit) | P(2x) | Break-even drift | OI |")
	w("|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, s := range r.BuyNow {
		c := s.Contract
		w("| %s | %g | %.2f | %.2f | %.0f%% | %.0f%% | %+.1f pts | %.2f | %s | %.0f%% | %.0f%% | %s | %d |",
			d(c.Expiry), c.Strike, c.Delta, c.MarketMid, 100*c.MarketIV, 100*c.ModelIV, 100*c.VolEdge, c.ModelPrice,
			pct(s.Stats.MeanReturn), 100*s.Stats.PProfit, 100*s.Stats.PDoubled, breakEvenText(s.BreakEvenDrift), c.OpenInterest)
	}
	w("")
	w("Vol edge is the model's forecast volatility minus the market's implied volatility. A negative edge means you are paying more volatility than the model expects to be delivered, which is the normal cost of owning options.")

	w("")
	w("## Ranked entry windows")
	w("")
	if len(r.Strategies) == 0 {
		w("Nothing qualified.")
	} else {
		w("Each row is a commitment: watch for the trigger between the two dates, buy the named contract the first time it fires, hold to expiry. One row per trigger, each showing the date range and contract that served it best.")
		w("")
		w("| # | Window | Trigger | Buy | Fill prob | Growth | Kelly | Return if filled | Median | P(profit) | Expected value | Avg cost | Break-even drift |")
		w("|---|---|---|---|---|---|---|---|---|---|---|---|---|")
		for i, s := range r.Strategies {
			w("| %d | %s to %s | %s | %s %g | %.0f%% | %+.4f | %.0f%% | %s | %s | %.0f%% | %s | %.2f | %s |",
				i+1, d(s.Window.Start), d(s.Window.End), triggerText(s.Trigger, r.Spot),
				d(s.Contract.Expiry), s.Contract.Strike, 100*s.Stats.PTrade,
				s.Stats.GrowthRate, 100*s.Stats.KellyFraction, pct(s.Stats.MeanReturn),
				pct(s.Stats.MedianReturn), 100*s.Stats.PProfit, pct(s.Stats.ExpectedValue),
				s.Stats.MeanCost, breakEvenText(s.BreakEvenDrift))
		}
		w("")
		w("Growth is expected log growth of capital when %.0f%% of it is committed to the trade, which is what the ranking maximises. Kelly is the stake that would maximise it; a zero there means no position size beats holding cash. Expected value is the fill probability times the return if filled, counting paths where you never trade as zero. Break-even drift is the annual return the stock needs for the plan to return nothing, which is the number to compare against your own view.", 100*RankingStake)
		w("")
		w("### Drift sensitivity")
		w("")
		w("The same plans, re-simulated under each assumed annual return for %s.", r.Symbol)
		w("")
		var head strings.Builder
		head.WriteString("| # | Buy |")
		for _, g := range r.DriftGrid {
			fmt.Fprintf(&head, " %+.0f%%/yr |", 100*g)
		}
		w("%s", head.String())
		w("|---|---|%s", strings.Repeat("---|", len(r.DriftGrid)))
		for i, s := range r.Strategies {
			var row strings.Builder
			fmt.Fprintf(&row, "| %d | %s %g |", i+1, d(s.Contract.Expiry), s.Contract.Strike)
			for _, v := range s.DriftCurve {
				fmt.Fprintf(&row, " %s |", pct(v))
			}
			w("%s", row.String())
		}
	}

	w("")
	w("## Method and limitations")
	w("")
	w("- Volatility: GARCH(1,1), variance targeting, Gaussian likelihood, fitted on %d daily log returns.", r.GARCH.n())
	w("- Event structure: implied variance is split into a diffusive term and one earnings bump by non-negative least squares across listed expiries. The earnings date is an estimate until the company confirms it.")
	w("- Paths: filtered historical simulation. Volatility clusters as the GARCH process dictates; shocks are drawn from the stock's own standardized residuals, so skew and fat tails survive; the earnings day carries an independent normal jump.")
	w("- Pricing at entry: Black-Scholes on the fitted surface, which is held sticky in strike space, plus half the current bid-ask spread as slippage. The surface does not respond to the simulated path, so a plan that only pays off through a volatility spike is not being credited for one.")
	w("- Payoff: European, held to expiry, no early exercise and no dividends.")
	w("- Ranking: expected log growth at a %.0f%% stake rather than expected return, so a plan is not rewarded for being a lottery ticket. An entry must leave at least %d trading days to expiry.", 100*RankingStake, minRunway)
	w("- Drift is the weakest input: a capped, shrunk trend estimate, not a forecast of news. The break-even drift and the sensitivity table are there so the reader can substitute their own view.")
	w("- Trading-day counts ignore exchange holidays. Nothing here is investment advice.")
	return b.String()
}

// n is the estimation sample size.
func (g GARCH) n() int { return len(g.Resid) }

func buyNowReturn(r *Result, c Contract) float64 {
	for _, s := range r.BuyNow {
		if s.Contract.Strike == c.Strike && s.Contract.ExpiryIdx == c.ExpiryIdx {
			return s.Stats.MeanReturn
		}
	}
	return 0
}

// breakEvenText renders the drift a plan needs to return nothing, marking
// the cases that fall outside the simulated grid.
func breakEvenText(b BreakEven) string {
	d := float64(b)
	switch {
	case math.IsInf(d, 1):
		return fmt.Sprintf("above %+.0f%%/yr", 100*DriftGrid[len(DriftGrid)-1])
	case math.IsInf(d, -1):
		return fmt.Sprintf("below %+.0f%%/yr", 100*DriftGrid[0])
	default:
		return fmt.Sprintf("%+.1f%%/yr", 100*d)
	}
}

func triggerText(t Trigger, spot float64) string {
	switch t.Kind {
	case TriggerDip:
		return fmt.Sprintf("dip to %.2f (%.0f%%)", spot*t.Level, 100*(t.Level-1))
	case TriggerBreakout:
		return fmt.Sprintf("rise to %.2f (+%.0f%%)", spot*t.Level, 100*(t.Level-1))
	default:
		return "enter at open"
	}
}

func monthlyMark(monthly bool) string {
	if monthly {
		return " (monthly)"
	}
	return ""
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
