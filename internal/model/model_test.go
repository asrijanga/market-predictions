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
	// A company to value: earning steadily above its cost of capital, with
	// a book value that still describes it and a multiple history to fit a
	// reversion speed to.
	shares := 1e9
	eps := spot / 20 // a trailing multiple of twenty
	quarters := make([]market.QuarterEPS, 0, 4)
	for i := 3; i >= 0; i-- {
		quarters = append(quarters, market.QuarterEPS{
			End: now.AddDate(0, -3*i, -10), Reported: eps / 4,
		})
	}
	funda := market.Fundamentals{
		Symbol: "TEST", Shares: shares, MarketCap: spot * shares,
		TrailingEPS:       eps,
		ForwardEPS:        []float64{eps * 1.08, eps * 1.16, eps * 1.25},
		QuarterlyEPS:      quarters,
		BookValuePerShare: spot / 3,
		DividendPerShare:  eps * 0.3,
		ReturnOnEquity:    0.18,
		NetIncome:         []float64{eps * shares},
		Equity:            []float64{spot / 3 * shares},
		FiscalEnds:        []string{now.Format("1/2/2006")},
	}
	peHistory := make([]float64, 0, len(bars))
	for _, b := range bars {
		peHistory = append(peHistory, b.Close/eps)
	}

	return &pack.Pack{
		Symbol: "TEST", AsOf: now, Price: spot, Benchmark: "SPY", HorizonDays: 63,
		Trend: trend, Bars: bars, ModelCloses: closes,
		EarningsDate: earnings, DaysToEarnings: quant.TradingDays(now, earnings),
		Expiries:     options.Summarize(quotes, spot, now, 0.04),
		Fundamentals: funda, Beta: 1.05, PEHistory: peHistory,
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
	if len(r.VolTerm) == 0 || len(r.Dist) == 0 {
		t.Fatalf("empty sections: volterm=%d dist=%d", len(r.VolTerm), len(r.Dist))
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
	if r.FairValue <= 0 {
		t.Fatalf("no fair value produced: %v", r.Warnings)
	}
	if math.Abs(r.Upside-(r.FairValue/r.Spot-1)) > 1e-9 {
		t.Errorf("upside %.4f does not match fair value %.2f against spot %.2f",
			r.Upside, r.FairValue, r.Spot)
	}
	if len(r.Valuation.Estimates) == 0 {
		t.Error("the fair value came from no model")
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
		"## Why ", "## What argues against it", "## What it looks worth",
		"## How long it might take", "## How much to trust this",
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
