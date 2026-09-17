package quant

import (
	"math"
	"testing"
	"time"

	"github.com/asrijanga/market-predictions/internal/market"
)

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestLinRegPerfectLine(t *testing.T) {
	y := []float64{1, 3, 5, 7, 9}
	slope, intercept, r2 := LinReg(y)
	if !near(slope, 2, 1e-12) || !near(intercept, 1, 1e-12) || !near(r2, 1, 1e-12) {
		t.Fatalf("slope=%v intercept=%v r2=%v", slope, intercept, r2)
	}
}

func TestLinRegFlat(t *testing.T) {
	slope, _, r2 := LinReg([]float64{4, 4, 4, 4})
	if slope != 0 || r2 != 0 {
		t.Fatalf("slope=%v r2=%v", slope, r2)
	}
}

func TestStdDevAndMean(t *testing.T) {
	xs := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	if !near(Mean(xs), 5, 1e-12) {
		t.Errorf("mean = %v", Mean(xs))
	}
	if !near(StdDev(xs), 2.13809, 1e-4) {
		t.Errorf("stddev = %v", StdDev(xs))
	}
}

func TestRSIExtremes(t *testing.T) {
	up := make([]float64, 30)
	for i := range up {
		up[i] = 100 + float64(i)
	}
	if got := RSI(up, 14); got != 100 {
		t.Errorf("RSI of monotonic rise = %v, want 100", got)
	}
	down := make([]float64, 30)
	for i := range down {
		down[i] = 100 - float64(i)
	}
	if got := RSI(down, 14); got != 0 {
		t.Errorf("RSI of monotonic fall = %v, want 0", got)
	}
	if got := RSI([]float64{1, 2}, 14); got != 50 {
		t.Errorf("RSI with insufficient data = %v, want 50", got)
	}
}

func TestMaxDrawdown(t *testing.T) {
	if got := MaxDrawdown([]float64{100, 120, 90, 110, 60, 100}); !near(got, 0.5, 1e-12) {
		t.Fatalf("got %v want 0.5", got)
	}
}

func TestTotalReturn(t *testing.T) {
	closes := []float64{100, 110, 121}
	if got := TotalReturn(closes, 2); !near(got, 0.21, 1e-12) {
		t.Fatalf("got %v", got)
	}
	if got := TotalReturn(closes, 5); got != 0 {
		t.Fatalf("out of range should be 0, got %v", got)
	}
}

func TestNormCDF(t *testing.T) {
	if !near(NormCDF(0), 0.5, 1e-12) || !near(NormCDF(1.96), 0.975, 1e-3) {
		t.Fatal("NormCDF mismatch")
	}
}

func TestThirdFriday(t *testing.T) {
	cases := []struct {
		y    int
		m    time.Month
		want int
	}{
		{2026, time.November, 20},
		{2026, time.December, 18},
		{2026, time.September, 18},
		{2027, time.January, 15},
	}
	for _, c := range cases {
		if got := ThirdFriday(c.y, c.m); got.Day() != c.want || got.Weekday() != time.Friday {
			t.Errorf("ThirdFriday(%d,%v) = %v, want day %d", c.y, c.m, got, c.want)
		}
	}
}

func TestTradingDays(t *testing.T) {
	from := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC) // Wednesday
	to := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)   // next Wednesday
	if got := TradingDays(from, to); got != 5 {
		t.Fatalf("got %d want 5", got)
	}
	if got := TradingDays(to, from); got != 0 {
		t.Fatalf("reverse range should be 0, got %d", got)
	}
}

func TestParseExpiry(t *testing.T) {
	d, err := ParseExpiry("2026-11")
	if err != nil || d.Day() != 20 {
		t.Fatalf("2026-11 -> %v, %v", d, err)
	}
	d, err = ParseExpiry("2026-11-13")
	if err != nil || d.Day() != 13 {
		t.Fatalf("2026-11-13 -> %v, %v", d, err)
	}
	if _, err := ParseExpiry("nov"); err == nil {
		t.Fatal("expected error")
	}
}

func TestSuggestStrike(t *testing.T) {
	cases := map[float64]float64{10.2: 10.5, 33.1: 34, 101: 102.5, 331.34: 335, 335: 335}
	for px, want := range cases {
		if got := SuggestStrike(px); !near(got, want, 1e-9) {
			t.Errorf("SuggestStrike(%v) = %v, want %v", px, got, want)
		}
	}
}

// syntheticBars generates n bars with a constant daily log drift plus a
// deterministic ripple so volatility is non-zero.
func syntheticBars(n int, drift float64) []market.Bar {
	bars := make([]market.Bar, n)
	p := 100.0
	for i := range bars {
		ripple := 0.01 * math.Sin(float64(i))
		p *= math.Exp(drift + ripple)
		bars[i] = market.Bar{Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i), Close: p, Volume: 1e6}
	}
	return bars
}

func TestAnalyzeUptrendBeatsDowntrend(t *testing.T) {
	bench := SeriesStats(Closes(syntheticBars(126, 0.0003)), 126)
	up, err := Analyze("UP", syntheticBars(126, 0.002), bench, 126, 63)
	if err != nil {
		t.Fatal(err)
	}
	down, err := Analyze("DOWN", syntheticBars(126, -0.002), bench, 126, 63)
	if err != nil {
		t.Fatal(err)
	}
	if up.Score <= down.Score {
		t.Fatalf("up score %v should exceed down score %v", up.Score, down.Score)
	}
	if up.ExpectedReturn <= 0 || up.ProbUp <= 0.5 {
		t.Errorf("uptrend forecast = %+v", up)
	}
	if up.LowPrice >= up.ExpectedPrice || up.HighPrice <= up.ExpectedPrice {
		t.Errorf("bands not ordered: %+v", up)
	}
	if !up.AboveSMA50 || up.R2 < 0.9 {
		t.Errorf("uptrend should sit above SMA50 with high R2: %+v", up)
	}
}

func TestAnalyzeRejectsShortHistory(t *testing.T) {
	if _, err := Analyze("X", syntheticBars(20, 0.001), Stats{}, 126, 63); err == nil {
		t.Fatal("expected error for short history")
	}
}

func TestRank(t *testing.T) {
	fs := []Forecast{{Symbol: "B", Score: 1}, {Symbol: "A", Score: 1}, {Symbol: "C", Score: 2}}
	Rank(fs)
	if fs[0].Symbol != "C" || fs[1].Symbol != "A" || fs[2].Symbol != "B" {
		t.Fatalf("order = %v %v %v", fs[0].Symbol, fs[1].Symbol, fs[2].Symbol)
	}
}
