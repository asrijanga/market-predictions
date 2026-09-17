package quant

import (
	"fmt"
	"math"
	"sort"

	"github.com/asrijanga/market-predictions/internal/market"
)

// Model parameters. They are heuristics chosen to be conservative rather
// than fitted, and are documented in the README.
const (
	// longRunDailyDrift is ~8%/yr in log terms, the anchor toward which the
	// benchmark drift is shrunk.
	longRunDailyDrift = 0.08 / TradingDaysPerYear
	// maxDailyDrift caps extrapolated drift at roughly ±100%/yr.
	maxDailyDrift = 1.0 / TradingDaysPerYear
	// Alpha shrink runs from minShrink (noisy trend) to minShrink+shrinkR2
	// (clean trend, R² → 1).
	minShrink = 0.30
	shrinkR2  = 0.40
)

// TradingDaysPerYear is the annualisation constant.
const TradingDaysPerYear = 252.0

// Stats summarises a price series over the lookback window.
type Stats struct {
	Drift float64 `json:"drift_daily"` // OLS slope of log price per trading day
	R2    float64 `json:"r2"`          // goodness of fit of that trend
	Vol   float64 `json:"vol_daily"`   // standard deviation of daily log returns
}

// Forecast is the full analysis of one symbol.
type Forecast struct {
	Symbol      string  `json:"symbol"`
	Price       float64 `json:"price"`
	Bars        int     `json:"bars"`
	HorizonDays int     `json:"horizon_days"`

	Return1M       float64 `json:"return_1m"`
	Return3M       float64 `json:"return_3m"`
	Return6M       float64 `json:"return_6m"`
	ExcessReturn6M float64 `json:"excess_return_6m"`
	Drift          float64 `json:"drift_daily"`
	R2             float64 `json:"trend_r2"`
	AnnualVol      float64 `json:"annual_vol"`
	RSI            float64 `json:"rsi_14"`
	MaxDrawdown    float64 `json:"max_drawdown"`
	AboveSMA20     bool    `json:"above_sma20"`
	AboveSMA50     bool    `json:"above_sma50"`
	VolumeRatio    float64 `json:"volume_ratio_20d"`

	ExpectedReturn float64  `json:"expected_return"`
	ExpectedPrice  float64  `json:"expected_price"`
	LowPrice       float64  `json:"low_price_1sd"`
	HighPrice      float64  `json:"high_price_1sd"`
	ProbUp         float64  `json:"prob_up"`
	Score          float64  `json:"score"`
	Strike         float64  `json:"suggested_strike"`
	Flags          []string `json:"flags,omitempty"`
}

// Closes extracts the close series from bars.
func Closes(bars []market.Bar) []float64 {
	out := make([]float64, len(bars))
	for i, b := range bars {
		out[i] = b.Close
	}
	return out
}

// SeriesStats fits the trend and volatility of the last lookback closes.
func SeriesStats(closes []float64, lookback int) Stats {
	if len(closes) > lookback {
		closes = closes[len(closes)-lookback:]
	}
	logs := make([]float64, len(closes))
	for i, c := range closes {
		logs[i] = math.Log(c)
	}
	slope, _, r2 := LinReg(logs)
	return Stats{Drift: slope, R2: r2, Vol: StdDev(LogReturns(closes))}
}

// Analyze evaluates one symbol against benchmark statistics over the given
// lookback window and projects it horizon trading days ahead.
func Analyze(symbol string, bars []market.Bar, bench Stats, lookback, horizon int) (Forecast, error) {
	if minBars := lookback * 4 / 5; len(bars) < minBars {
		return Forecast{}, fmt.Errorf("%s: %d bars, need at least %d", symbol, len(bars), minBars)
	}
	if len(bars) > lookback {
		bars = bars[len(bars)-lookback:]
	}
	closes := Closes(bars)
	price := closes[len(closes)-1]
	st := SeriesStats(closes, lookback)

	f := Forecast{
		Symbol:      symbol,
		Price:       price,
		Bars:        len(bars),
		HorizonDays: horizon,
		Return1M:    TotalReturn(closes, 21),
		Return3M:    TotalReturn(closes, 63),
		Return6M:    TotalReturn(closes, len(closes)-1),
		Drift:       st.Drift,
		R2:          st.R2,
		AnnualVol:   st.Vol * math.Sqrt(TradingDaysPerYear),
		RSI:         RSI(closes, 14),
		MaxDrawdown: MaxDrawdown(closes),
		AboveSMA20:  price > SMA(closes, 20),
		AboveSMA50:  price > SMA(closes, 50),
	}
	// Benchmark 6-month return in log-drift terms for a like-for-like excess.
	f.ExcessReturn6M = f.Return6M - math.Expm1(bench.Drift*float64(len(closes)-1))

	vols := make([]float64, len(bars))
	for i, b := range bars {
		vols[i] = float64(b.Volume)
	}
	if avg := Mean(vols); avg > 0 {
		f.VolumeRatio = SMA(vols, 20) / avg
	}

	drift := ShrunkDrift(st, bench)

	h := float64(horizon)
	muH := drift * h
	sigmaH := st.Vol * math.Sqrt(h)
	f.ExpectedReturn = math.Expm1(muH)
	f.ExpectedPrice = price * math.Exp(muH)
	f.LowPrice = price * math.Exp(muH-sigmaH)
	f.HighPrice = price * math.Exp(muH+sigmaH)
	if sigmaH > 0 {
		f.ProbUp = NormCDF(muH / sigmaH)
	} else {
		f.ProbUp = 0.5
	}
	f.Strike = SuggestStrike(price)
	f.Score, f.Flags = score(f, muH, sigmaH)
	return f, nil
}

// ShrunkDrift returns the daily log drift used for projection. The
// benchmark's fitted drift is pulled halfway toward a long-run anchor, the
// stock's alpha over that is shrunk in proportion to its trend quality
// (R²), and the result is capped so nothing extrapolates absurdly.
func ShrunkDrift(stock, bench Stats) float64 {
	benchDrift := 0.5*bench.Drift + 0.5*longRunDailyDrift
	shrink := minShrink + shrinkR2*clamp(stock.R2, 0, 1)
	return clamp(benchDrift+shrink*(stock.Drift-benchDrift), -maxDailyDrift, maxDailyDrift)
}

// score converts a forecast into a single call-attractiveness number. The
// core is the horizon Sharpe-like ratio; trend confirmation adds, and
// stretched or broken charts subtract.
func score(f Forecast, muH, sigmaH float64) (float64, []string) {
	var flags []string
	s := 0.0
	if sigmaH > 0 {
		s = muH / sigmaH
	}
	s += 0.5 * f.R2

	switch {
	case f.AboveSMA20 && f.AboveSMA50:
		s += 0.25
	case !f.AboveSMA50:
		s -= 0.5
		flags = append(flags, "below-50d")
	}
	switch {
	case f.RSI > 80:
		s -= 0.4
		flags = append(flags, "overbought")
	case f.RSI > 70:
		s -= 0.15
		flags = append(flags, "extended")
	}
	if f.MaxDrawdown > 0.30 {
		s -= 0.3
		flags = append(flags, "deep-drawdown")
	}
	if f.Return1M < -0.10 {
		s -= 0.3
		flags = append(flags, "recent-selloff")
	}
	if f.AnnualVol > 0.80 {
		s -= 0.3
		flags = append(flags, "extreme-vol")
	}
	if f.VolumeRatio > 1.5 {
		s += 0.1
		flags = append(flags, "volume-surge")
	}
	return s, flags
}

// SuggestStrike returns the first listed strike at or above price using
// standard US equity strike increments.
func SuggestStrike(price float64) float64 {
	var incr float64
	switch {
	case price < 25:
		incr = 0.5
	case price < 50:
		incr = 1
	case price < 200:
		incr = 2.5
	default:
		incr = 5
	}
	return math.Ceil(price/incr-1e-9) * incr
}

// Rank sorts forecasts by descending score, then symbol for determinism.
func Rank(fs []Forecast) {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Score != fs[j].Score {
			return fs[i].Score > fs[j].Score
		}
		return fs[i].Symbol < fs[j].Symbol
	})
}

func clamp(x, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, x))
}
