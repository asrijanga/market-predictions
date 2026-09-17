// Package model turns a data pack into a call-option timing decision using
// statistical models only: a GARCH(1,1) volatility forecast, a decomposition
// of the implied-volatility surface into diffusive and event variance, and a
// filtered historical simulation of forward price paths that is used to rank
// candidate entry windows by expected return.
package model

import (
	"math"

	"github.com/asrijanga/market-predictions/internal/quant"
)

// GARCH is a fitted GARCH(1,1) model with variance targeting, so that the
// unconditional variance equals the sample variance and only the two
// persistence parameters are estimated.
//
//	r_t     = mu + e_t,  e_t = sigma_t * z_t
//	sigma²_t = omega + alpha*e²_{t-1} + beta*sigma²_{t-1}
//	omega    = uncondVar * (1 - alpha - beta)
type GARCH struct {
	Mean      float64   `json:"mean"`
	Alpha     float64   `json:"alpha"`
	Beta      float64   `json:"beta"`
	Omega     float64   `json:"omega"`
	UncondVar float64   `json:"uncond_var"`
	LogLik    float64   `json:"log_likelihood"`
	CondVar   []float64 `json:"-"` // filtered variance, one per return
	Resid     []float64 `json:"-"` // standardized residuals e_t/sigma_t
}

// Persistence is alpha+beta: how slowly a volatility shock decays.
func (g GARCH) Persistence() float64 { return g.Alpha + g.Beta }

// HalfLife is the number of days for a variance shock to decay by half.
func (g GARCH) HalfLife() float64 {
	p := g.Persistence()
	if p <= 0 || p >= 1 {
		return math.Inf(1)
	}
	return math.Log(0.5) / math.Log(p)
}

// FitGARCH estimates the model by maximum likelihood over a refined grid of
// (alpha, beta). Variance targeting keeps the search two-dimensional, which
// is both fast and numerically stable on the few hundred observations a
// single stock's history provides.
func FitGARCH(returns []float64) GARCH {
	n := len(returns)
	if n < 60 {
		v := math.Max(quant.StdDev(returns)*quant.StdDev(returns), 1e-8)
		return GARCH{Mean: quant.Mean(returns), Alpha: 0.08, Beta: 0.90, Omega: v * 0.02, UncondVar: v}
	}
	mean := quant.Mean(returns)
	target := 0.0
	for _, r := range returns {
		d := r - mean
		target += d * d
	}
	target /= float64(n - 1)
	if target <= 0 {
		target = 1e-8
	}

	best := GARCH{Mean: mean, UncondVar: target, Alpha: 0.05, Beta: 0.90, LogLik: math.Inf(-1)}
	search := func(a0, a1, b0, b1, step float64) {
		for a := a0; a <= a1+1e-12; a += step {
			for b := b0; b <= b1+1e-12; b += step {
				if a <= 0 || b < 0 || a+b >= 0.999 {
					continue
				}
				if ll := logLik(returns, mean, target, a, b); ll > best.LogLik {
					best.Alpha, best.Beta, best.LogLik = a, b, ll
				}
			}
		}
	}
	search(0.01, 0.35, 0.40, 0.98, 0.01)
	search(math.Max(0.002, best.Alpha-0.01), best.Alpha+0.01, math.Max(0, best.Beta-0.01), math.Min(0.99, best.Beta+0.01), 0.002)

	best.Omega = target * (1 - best.Alpha - best.Beta)
	best.CondVar, _ = filter(returns, mean, target, best.Alpha, best.Beta)
	best.Resid = make([]float64, n)
	for i, r := range returns {
		best.Resid[i] = (r - mean) / math.Sqrt(best.CondVar[i])
	}
	return best
}

// filter runs the variance recursion and returns the conditional variance
// for each observation plus the Gaussian log-likelihood.
func filter(returns []float64, mean, target, alpha, beta float64) ([]float64, float64) {
	omega := target * (1 - alpha - beta)
	cv := make([]float64, len(returns))
	sigma2 := target
	var ll float64
	for i, r := range returns {
		cv[i] = sigma2
		e := r - mean
		ll += -0.5 * (math.Log(2*math.Pi) + math.Log(sigma2) + e*e/sigma2)
		sigma2 = omega + alpha*e*e + beta*sigma2
		if sigma2 <= 0 || math.IsNaN(sigma2) {
			sigma2 = target
		}
	}
	return cv, ll
}

func logLik(returns []float64, mean, target, alpha, beta float64) float64 {
	_, ll := filter(returns, mean, target, alpha, beta)
	return ll
}

// NextVar is the one-step-ahead conditional variance forecast. The
// variance recursion is re-run over the supplied history, so a model fitted
// on a shorter sample can still be applied to a longer one - which is what
// walk-forward scoring needs.
func (g GARCH) NextVar(returns []float64) float64 {
	if len(returns) == 0 || g.UncondVar <= 0 {
		return g.UncondVar
	}
	cv, _ := filter(returns, g.Mean, g.UncondVar, g.Alpha, g.Beta)
	last := len(returns) - 1
	e := returns[last] - g.Mean
	return g.Omega + g.Alpha*e*e + g.Beta*cv[last]
}

// VarPath returns the expected conditional variance for each of the next h
// days, converging geometrically to the unconditional level.
func (g GARCH) VarPath(next float64, h int) []float64 {
	out := make([]float64, h)
	p := g.Persistence()
	for i := range out {
		out[i] = g.UncondVar + math.Pow(p, float64(i))*(next-g.UncondVar)
	}
	return out
}

// AvgVol returns the annualised volatility implied by the average variance
// over the next h days, which is what an option expiring in h days prices.
func (g GARCH) AvgVol(next float64, h int) float64 {
	if h <= 0 {
		return math.Sqrt(math.Max(next, 0) * quant.TradingDaysPerYear)
	}
	return math.Sqrt(quant.Mean(g.VarPath(next, h)) * quant.TradingDaysPerYear)
}

// EWMAVar is the RiskMetrics exponentially weighted variance estimate.
func EWMAVar(returns []float64, lambda float64) float64 {
	if len(returns) == 0 {
		return 0
	}
	v := returns[0] * returns[0]
	for _, r := range returns[1:] {
		v = lambda*v + (1-lambda)*r*r
	}
	return v
}

// VolScore reports out-of-sample forecast accuracy for one volatility model.
type VolScore struct {
	Name  string  `json:"name"`
	QLIKE float64 `json:"qlike"` // lower is better
	RMSE  float64 `json:"rmse"`  // on daily variance
}

// ScoreVolModels walks forward over the last oos observations and compares
// one-day-ahead variance forecasts from GARCH, EWMA, a rolling 20-day
// window and a constant (full-sample) variance. Losses are QLIKE, which is
// robust to the fact that realised variance is observed only through the
// squared return, plus the root mean squared error on daily variance.
//
// The GARCH model is refit every 21 observations, which keeps the walk
// honest without refitting at every step. The constant-variance row is the
// benchmark that tells you whether conditional volatility modelling is
// earning its keep on this stock at all.
func ScoreVolModels(returns []float64, oos int) []VolScore {
	n := len(returns)
	if oos <= 0 || n < oos+120 {
		return nil
	}
	names := []string{"GARCH(1,1)", "EWMA lambda=0.94", "Rolling 20-day", "Constant variance"}
	loss := make([][]float64, len(names))
	sqErr := make([][]float64, len(names))

	var g GARCH
	start := n - oos
	for t := start; t < n; t++ {
		hist := returns[:t]
		if (t-start)%21 == 0 {
			g = FitGARCH(hist)
		}
		actual := returns[t] * returns[t]
		if actual <= 0 {
			continue
		}
		forecasts := []float64{
			g.NextVar(hist),
			EWMAVar(hist, 0.94),
			rollingVar(hist, 20),
			rollingVar(hist, len(hist)),
		}
		for i, f := range forecasts {
			if f <= 0 {
				continue
			}
			loss[i] = append(loss[i], actual/f-math.Log(actual/f)-1)
			sqErr[i] = append(sqErr[i], (actual-f)*(actual-f))
		}
	}
	out := make([]VolScore, 0, len(names))
	for i, name := range names {
		out = append(out, VolScore{Name: name, QLIKE: quant.Mean(loss[i]), RMSE: math.Sqrt(quant.Mean(sqErr[i]))})
	}
	return out
}

func rollingVar(returns []float64, n int) float64 {
	if len(returns) < n {
		n = len(returns)
	}
	w := returns[len(returns)-n:]
	s := quant.StdDev(w)
	return s * s
}
