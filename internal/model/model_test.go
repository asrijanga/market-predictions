package model

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/asrijanga/market-predictions/internal/market"
	"github.com/asrijanga/market-predictions/internal/options"
	"github.com/asrijanga/market-predictions/internal/pack"
	"github.com/asrijanga/market-predictions/internal/quant"
)

// testPack builds a pack with a clean synthetic price history and a listed
// option chain priced off a known volatility, so the model has something
// self-consistent to fit.
func testPack(t *testing.T) *pack.Pack {
	t.Helper()
	now := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	const n = 500
	closes := make([]float64, n)
	bars := make([]market.Bar, n)
	p := 100.0
	d := now.AddDate(0, 0, -n*7/5)
	for i := range closes {
		p *= math.Exp(0.0004 + 0.012*math.Sin(float64(i)*0.7))
		closes[i] = p
		for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			d = d.AddDate(0, 0, 1)
		}
		bars[i] = market.Bar{Date: d, Open: p, High: p * 1.01, Low: p * 0.99, Close: p, Volume: 2e6}
		d = d.AddDate(0, 0, 1)
	}
	spot := closes[n-1]
	earnings := now.AddDate(0, 0, 42) // ~30 trading days out

	var quotes []market.OptionQuote
	for _, exp := range []time.Time{
		quant.ThirdFriday(2026, time.October), quant.ThirdFriday(2026, time.November),
		quant.ThirdFriday(2026, time.December), quant.ThirdFriday(2027, time.January),
	} {
		days := quant.TradingDays(now, exp)
		years := float64(days) / quant.TradingDaysPerYear
		v := 0.28*0.28*years + 0.0
		if exp.After(earnings) {
			v += 0.05 * 0.05
		}
		iv := math.Sqrt(v / years)
		for k := math.Round(spot*0.95/5) * 5; k <= spot*1.12; k += 5 {
			c := options.CallPrice(spot, k, years, 0.04, iv)
			pt := options.PutPrice(spot, k, years, 0.04, iv)
			quotes = append(quotes, market.OptionQuote{
				Expiry: exp, Strike: k,
				Call: market.OptionSide{Bid: c * 0.99, Ask: c * 1.01, OpenInterest: 5000, Volume: 500},
				Put:  market.OptionSide{Bid: pt * 0.99, Ask: pt * 1.01, OpenInterest: 5000, Volume: 500},
			})
		}
	}
	bench := quant.SeriesStats(closes, 126)
	trend, err := quant.Analyze("TEST", bars, bench, 126, 63)
	if err != nil {
		t.Fatal(err)
	}
	return &pack.Pack{
		Symbol: "TEST", AsOf: now, Price: spot, Benchmark: "SPY", HorizonDays: 63,
		Trend: trend, Bars: bars, ModelCloses: closes,
		EarningsDate: earnings, DaysToEarnings: quant.TradingDays(now, earnings),
		Expiries: options.Summarize(quotes, spot, now, 0.04),
	}
}

func TestAnalyzeEndToEnd(t *testing.T) {
	p := testPack(t)
	o := Defaults()
	o.Paths = 4000
	r, err := Analyze(p, o)
	if err != nil {
		t.Fatal(err)
	}
	if r.Symbol != "TEST" || r.Spot != p.Price {
		t.Fatalf("result header wrong: %+v", r.Symbol)
	}
	if !r.IV.Fitted {
		t.Error("the surface should fit on a synthetic chain")
	}
	if math.Abs(r.IV.BaseVol-0.28) > 0.02 {
		t.Errorf("base vol = %.3f, want ~0.28", r.IV.BaseVol)
	}
	if math.Abs(r.ImpliedMove-0.05) > 0.015 {
		t.Errorf("implied earnings move = %.3f, want ~0.05", r.ImpliedMove)
	}
	if r.EarningsIdx <= 0 {
		t.Error("earnings index should be set")
	}
	if len(r.Contracts) == 0 || len(r.BuyNow) == 0 || len(r.VolTerm) == 0 || len(r.Dist) == 0 {
		t.Fatalf("empty sections: contracts=%d buynow=%d volterm=%d dist=%d",
			len(r.Contracts), len(r.BuyNow), len(r.VolTerm), len(r.Dist))
	}
	// The signal drift must stay inside the risk premium band either side
	// of the risk-free rate, however hard the synthetic series trends.
	if r.AnnualDrift > o.Rate+o.PremiumSpan+1e-9 || r.AnnualDrift < o.Rate-o.PremiumSpan-1e-9 {
		t.Errorf("drift %.3f outside the band %.3f ± %.3f", r.AnnualDrift, o.Rate, o.PremiumSpan)
	}
	if r.Stance == "" || len(r.Signals) < 8 {
		t.Errorf("stance %q from %d signals", r.Stance, len(r.Signals))
	}
	if len(r.Outlooks) != 2 || r.Outlooks[0].Days != 63 || r.Outlooks[1].Days != 252 {
		t.Fatalf("outlooks = %+v", r.Outlooks)
	}
	for _, out := range r.Outlooks {
		if out.Direction != "up" && out.Direction != "down" {
			t.Errorf("no direction for %s", out.Name)
		}
		if !(out.Low < out.Median && out.Median < out.High) {
			t.Errorf("outlook band out of order: %+v", out)
		}
		if out.ProbUp <= 0 || out.ProbUp >= 1 {
			t.Errorf("implausible P(up) %v", out.ProbUp)
		}
	}
	// A year of paths must be wider than a quarter of them.
	if q, y := r.Outlooks[0], r.Outlooks[1]; (y.High - y.Low) <= (q.High - q.Low) {
		t.Error("the one-year band should be wider than the one-quarter band")
	}
	// Distribution should widen with time and stay ordered.
	for _, row := range r.Dist {
		if !(row.P5 < row.P25 && row.P25 < row.Median && row.Median < row.P75 && row.P75 < row.P95) {
			t.Errorf("quantiles out of order: %+v", row)
		}
	}
	if len(r.Dist) > 1 {
		first, last := r.Dist[0], r.Dist[len(r.Dist)-1]
		if (last.P95 - last.P5) <= (first.P95 - first.P5) {
			t.Error("the distribution should widen with horizon")
		}
	}
	for _, s := range r.Strategies {
		if s.Stats.PTrade < o.MinPTrade {
			t.Errorf("plan below the fill threshold: %+v", s.Stats)
		}
		if len(s.DriftCurve) != len(DriftGrid) {
			t.Errorf("missing drift sensitivity: %d points", len(s.DriftCurve))
		}
		if s.Window.StartIdx > s.Contract.ExpiryIdx-minRunway {
			t.Error("plan entered inside the runway")
		}
	}
}

func TestDriftCurveIsMonotonicForALongCall(t *testing.T) {
	p := testPack(t)
	o := Defaults()
	o.Paths = 4000
	r, err := Analyze(p, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.BuyNow) == 0 {
		t.Fatal("no contracts")
	}
	curve := r.BuyNow[0].DriftCurve
	// A long call is worth more the faster the stock is assumed to compound;
	// the simulated curve should rise across the grid.
	if curve[0] >= curve[len(curve)-1] {
		t.Errorf("drift curve should rise with drift: %v", curve)
	}
}

func TestAnalyzeRiskNeutralDriftIsWorseThanTrend(t *testing.T) {
	p := testPack(t)
	base, rn := Defaults(), Defaults()
	base.Paths, rn.Paths = 4000, 4000
	rn.DriftMode = DriftRiskNeutral
	a, err := Analyze(p, base)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Analyze(p, rn)
	if err != nil {
		t.Fatal(err)
	}
	if b.AnnualDrift >= a.AnnualDrift {
		t.Errorf("risk-neutral drift %.3f should be below the capped trend %.3f", b.AnnualDrift, a.AnnualDrift)
	}
	if a.BuyNow[0].Stats.MeanReturn <= b.BuyNow[0].Stats.MeanReturn {
		t.Error("the same call should be worth less under a risk-neutral drift")
	}
}

func TestRenderSummaryAnswersTheQuestion(t *testing.T) {
	p := testPack(t)
	o := Defaults()
	o.Paths = 2000
	r, err := Analyze(p, o)
	if err != nil {
		t.Fatal(err)
	}
	md := Render(r)
	for _, want := range []string{
		"# TEST is ", "## Direction", "Next quarter", "Next year",
		"## Why ", "## What argues against it", "## If you are buying calls",
		"## How much to trust this",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("summary is missing %q", want)
		}
	}
	// The summary is the short answer; the evidence tables belong in detail.
	for _, unwanted := range []string{"QLIKE", "Drift sensitivity", "Contract screen"} {
		if strings.Contains(md, unwanted) {
			t.Errorf("summary should not carry %q", unwanted)
		}
	}
	assertNoFormatErrors(t, md)
}

func TestRenderDetailKeepsTheEvidence(t *testing.T) {
	p := testPack(t)
	o := Defaults()
	o.Paths = 2000
	r, err := Analyze(p, o)
	if err != nil {
		t.Fatal(err)
	}
	md := RenderDetail(r)
	for _, want := range []string{
		"# TEST model detail", "## Signal detail", "## Volatility model", "GARCH(1,1)",
		"## Implied volatility surface", "## Simulated price distribution",
		"## Contract screen", "## Ranked entry windows", "### Drift sensitivity",
		"## Method and limitations",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("detail is missing %q", want)
		}
	}
	assertNoFormatErrors(t, md)
}

func assertNoFormatErrors(t *testing.T, md string) {
	t.Helper()
	if strings.Contains(md, "MISSING") || strings.Contains(md, "%!") {
		t.Error("report contains a formatting error")
	}
}

func TestBreakEvenTextHandlesOutOfRange(t *testing.T) {
	if got := breakEvenText(BreakEven(math.Inf(1))); !strings.Contains(got, "above") {
		t.Errorf("got %q", got)
	}
	if got := breakEvenText(BreakEven(math.Inf(-1))); !strings.Contains(got, "below") {
		t.Errorf("got %q", got)
	}
	if got := breakEvenText(BreakEven(0.075)); got != "+7.5%/yr" {
		t.Errorf("got %q", got)
	}
}
