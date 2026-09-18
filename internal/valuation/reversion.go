package valuation

import (
	"math"
	"math/rand/v2"
	"slices"
)

// Reversion describes how quickly the gap between price and fair value has
// historically closed, and how long it should take from here.
type Reversion struct {
	// Kappa is the speed of mean reversion, per year: the fraction of the
	// remaining gap pulled back each year.
	Kappa float64 `json:"kappa"`
	// HalfLife is how long it takes to close half the gap, in months.
	HalfLife float64 `json:"half_life_months"`
	// MedianMonths is how long it takes to first reach fair value in half
	// of simulated paths. Zero means most paths did not get there inside
	// the horizon.
	MedianMonths float64 `json:"median_months"`
	// WithinYear is the fraction of paths reaching fair value inside twelve
	// months. The fraction arriving inside five years is close to one for
	// anything that reverts at all, so it distinguishes nothing.
	WithinYear float64 `json:"within_year"`
	// Fitted says whether Kappa came from this stock's own history or from
	// the fallback.
	Fitted bool `json:"fitted"`
}

// FitReversion estimates the speed at which a valuation ratio returns to
// its own average.
//
// The ratio is modelled as an Ornstein-Uhlenbeck process in logs,
//
//	dx = κ(μ − x)dt + σ dW
//
// which discretises to x_{t+1} − x_t = κ(μ − x_t)Δt + ε. Regressing the
// change on the level therefore recovers κ directly: the slope is −κΔt.
// This is the standard estimator and it needs nothing but the history.
//
// A stock whose valuation wanders without returning gives a slope at or
// above zero, which is not a slow reversion but an absent one, and is
// reported as unfitted rather than as a very large number.
func FitReversion(ratios []float64, perYear float64) (kappa float64, sigma float64, ok bool) {
	if len(ratios) < 60 || perYear <= 0 {
		return 0, 0, false
	}
	xs := make([]float64, 0, len(ratios))
	for _, r := range ratios {
		if r <= 0 || math.IsNaN(r) || math.IsInf(r, 0) {
			continue
		}
		xs = append(xs, math.Log(r))
	}
	if len(xs) < 60 {
		return 0, 0, false
	}

	// OLS of Δx on x, by closed-form sums.
	n := float64(len(xs) - 1)
	var sx, sy, sxx, sxy float64
	for i := 0; i < len(xs)-1; i++ {
		x, y := xs[i], xs[i+1]-xs[i]
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0, 0, false
	}
	slope := (n*sxy - sx*sy) / den
	intercept := (sy - slope*sx) / n

	// Residual scale, for the simulation.
	var ss float64
	for i := 0; i < len(xs)-1; i++ {
		e := (xs[i+1] - xs[i]) - (intercept + slope*xs[i])
		ss += e * e
	}
	sigma = math.Sqrt(ss/n) * math.Sqrt(perYear)

	if slope >= 0 {
		return 0, sigma, false // no reversion in this sample
	}
	kappa = -slope * perYear
	// A half-life under a month is fitting noise; over a decade is not a
	// statement anyone should act on. Either way the fit is not usable.
	if kappa <= 0.05 || kappa > 12 {
		return kappa, sigma, false
	}
	return kappa, sigma, true
}

// TimeToValue simulates how long the price takes to first reach fair value.
//
// The gap is what mean-reverts, so the simulation runs on the gap rather
// than on the price: x is log(price/fair value), pulled toward zero at
// speed κ with volatility σ. Reporting the median first passage rather
// than the expectation matters, because a process that reverts slowly has
// paths that never arrive and an expectation dragged to infinity by them.
func TimeToValue(gap, kappa, sigma float64, horizonYears float64, paths int, seed uint64) Reversion {
	out := Reversion{Kappa: kappa, Fitted: kappa > 0}
	if kappa > 0 {
		out.HalfLife = math.Ln2 / kappa * 12
	}
	if kappa <= 0 || sigma <= 0 || paths <= 0 || horizonYears <= 0 || gap == 0 {
		return out
	}

	const stepsPerYear = 52
	steps := int(horizonYears * stepsPerYear)
	dt := 1.0 / stepsPerYear
	sd := sigma * math.Sqrt(dt)
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b9))

	hits := make([]float64, 0, paths)
	for p := 0; p < paths; p++ {
		x := gap
		for s := 1; s <= steps; s++ {
			x += kappa*(0-x)*dt + sd*rng.NormFloat64()
			// Fair value is reached the first time the gap changes sign or
			// touches zero, whichever way it started.
			if (gap > 0 && x <= 0) || (gap < 0 && x >= 0) {
				hits = append(hits, float64(s)*dt*12)
				break
			}
		}
	}
	var withinYear int
	for _, h := range hits {
		if h <= 12 {
			withinYear++
		}
	}
	out.WithinYear = float64(withinYear) / float64(paths)
	if float64(len(hits))/float64(paths) >= 0.5 {
		// The median of the hitting times, taken over all paths: with more
		// than half arriving, the median arrival is well defined.
		slices.Sort(hits)
		idx := int(float64(paths) * 0.5)
		if idx < len(hits) {
			out.MedianMonths = hits[idx]
		}
	}
	return out
}
