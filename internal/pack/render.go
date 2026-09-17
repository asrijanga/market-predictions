package pack

import (
	"fmt"
	"strings"
	"time"
)

// Render produces the Markdown brief handed to the analyst. It is written
// for a reader, not a parser: dense tables, percentages, dates spelled out.
func Render(p *Pack) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	d := func(t time.Time) string { return t.Format("2006-01-02") }
	pct := func(x float64) string { return fmt.Sprintf("%+.1f%%", 100*x) }

	w("# %s data pack (as of %s)", p.Symbol, d(p.AsOf))
	w("")
	w("Spot %.2f. Projection horizon %d trading days (about %s). Benchmark %s.", p.Price, p.HorizonDays, d(p.TargetDate), p.Benchmark)
	if len(p.Warnings) > 0 {
		w("")
		w("Data warnings: %s", strings.Join(p.Warnings, "; "))
	}

	w("")
	w("## Price context")
	w("")
	w("| Metric | Value |")
	w("|---|---|")
	y := p.Year
	w("| 1y return | %s (benchmark %s) |", pct(y.Return1Y), pct(y.BenchmarkReturn1Y))
	w("| 6m / 3m / 1m return | %s / %s / %s |", pct(p.Trend.Return6M), pct(p.Trend.Return3M), pct(p.Trend.Return1M))
	w("| 52w high / low | %.2f / %.2f (%s from high) |", y.High52, y.Low52, pct(y.FromHighPct))
	w("| 200d SMA | %.2f (%s) |", y.SMA200, aboveBelow(y.AboveSMA200))
	w("| 50d / 20d SMA position | %s / %s |", aboveBelow(p.Trend.AboveSMA50), aboveBelow(p.Trend.AboveSMA20))
	w("| 6m trend | %+.1f%%/yr fitted drift, R² %.2f |", 100*p.Trend.Drift*252, p.Trend.R2)
	w("| RSI(14) | %.0f |", p.Trend.RSI)
	w("| 6m max drawdown | %.1f%% |", 100*p.Trend.MaxDrawdown)
	w("| Realized vol 20d / 1y | %.0f%% / %.0f%% (20d vol at %.0fth percentile of the year) |", 100*y.RealizedVol20, 100*y.RealizedVol1Y, 100*y.RealizedVolPctl)
	w("| Avg daily dollar volume | $%.0fM |", y.AvgDollarVolume/1e6)
	w("| Momentum model (6m) | expected %s by %s, P(up) %.0f%%, 1σ band %.0f–%.0f, score %.2f, flags: %s |",
		pct(p.Trend.ExpectedReturn), d(p.TargetDate), 100*p.Trend.ProbUp, p.Trend.LowPrice, p.Trend.HighPrice, p.Trend.Score, orNone(strings.Join(p.Trend.Flags, ", ")))

	w("")
	w("## Calendar")
	w("")
	if !p.EarningsDate.IsZero() {
		w("Next earnings: %s (%d trading days away, estimated by Nasdaq/Zacks).", d(p.EarningsDate), p.DaysToEarnings)
	} else {
		w("Next earnings: unknown.")
	}
	for _, e := range p.Events {
		w("- %s %s", d(e.Date), e.Name)
	}

	w("")
	w("## Option chain (IV from bid/ask mid)")
	w("")
	if len(p.Expiries) == 0 {
		w("No option data.")
	}
	w("Term structure, all expiries:")
	w("")
	for _, e := range p.Expiries {
		kind := "weekly"
		if e.Monthly {
			kind = "monthly"
		}
		w("- %s (%s, %d trading days): ATM %.0f, call IV %.0f%%, put IV %.0f%%, straddle-implied move ±%.1f%%, put/call OI %.2f",
			d(e.Expiry), kind, e.Days, e.ATMStrike, 100*e.ATMCallIV, 100*e.ATMPutIV, 100*e.StraddleMovePct, e.PutCallOIRatio)
	}
	w("")
	w("Call strikes (ATM to ~10%% OTM) for monthly expiries:")
	w("")
	for _, e := range p.Expiries {
		if !e.Monthly || e.Days < 5 {
			continue
		}
		w("### %s (%d trading days)", d(e.Expiry), e.Days)
		w("")
		w("| Strike | OTM | Mid | Spread | IV | Delta | Breakeven | OI | Vol |")
		w("|---|---|---|---|---|---|---|---|---|")
		for _, c := range e.Calls {
			w("| %.1f | %s | %.2f | %.0f%% | %.0f%% | %.2f | %.2f (%s) | %d | %d |",
				c.Strike, pct(c.Moneyness), c.Mid, 100*c.SpreadPct, 100*c.IV, c.Delta, c.Breakeven, pct(c.BreakevenPct), c.OpenInterest, c.Volume)
		}
		w("")
	}

	w("## Weekly closes (last 52 weeks)")
	w("")
	w("| Week ending | Close | Change | High | Low | Volume |")
	w("|---|---|---|---|---|---|")
	for i, wb := range p.Weekly {
		chg := ""
		if i > 0 && p.Weekly[i-1].Close > 0 {
			chg = pct(wb.Close/p.Weekly[i-1].Close - 1)
		}
		w("| %s | %.2f | %s | %.2f | %.2f | %.1fM |", d(wb.Date), wb.Close, chg, wb.High, wb.Low, float64(wb.Volume)/1e6)
	}

	w("")
	w("## Last 15 sessions")
	w("")
	w("| Date | Open | High | Low | Close | Volume |")
	w("|---|---|---|---|---|---|")
	start := max(0, len(p.Bars)-15)
	for _, bar := range p.Bars[start:] {
		w("| %s | %.2f | %.2f | %.2f | %.2f | %.1fM |", d(bar.Date), bar.Open, bar.High, bar.Low, bar.Close, float64(bar.Volume)/1e6)
	}

	w("")
	w("## SEC filings (last 12 months, 8-K/10-Q/10-K)")
	w("")
	if len(p.Filings) == 0 {
		w("None available.")
	}
	for _, f := range p.Filings {
		extra := f.Description
		if f.Items != "" {
			extra += " items " + f.Items
		}
		w("- %s %s %s", d(f.Filed), f.Form, strings.TrimSpace(extra))
	}

	w("")
	w("## Headlines (newest first)")
	w("")
	if len(p.Headlines) == 0 {
		w("None available.")
	}
	for _, h := range p.Headlines {
		w("- %s [%s] %s", d(h.Published), h.Source, h.Title)
	}
	return b.String()
}

func aboveBelow(above bool) string {
	if above {
		return "above"
	}
	return "below"
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
