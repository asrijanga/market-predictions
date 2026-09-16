// Package quant implements the indicators, projection model and ranking
// used to identify call-option candidates.
package quant

import "math"

// LogReturns returns ln(p[i]/p[i-1]) for i in 1..n-1.
func LogReturns(closes []float64) []float64 {
	if len(closes) < 2 {
		return nil
	}
	out := make([]float64, len(closes)-1)
	for i := 1; i < len(closes); i++ {
		out[i-1] = math.Log(closes[i] / closes[i-1])
	}
	return out
}

// Mean returns the arithmetic mean, or 0 for an empty slice.
func Mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// StdDev returns the sample standard deviation, or 0 for fewer than 2 points.
func StdDev(xs []float64) float64 {
	n := len(xs)
	if n < 2 {
		return 0
	}
	m := Mean(xs)
	var ss float64
	for _, x := range xs {
		d := x - m
		ss += d * d
	}
	return math.Sqrt(ss / float64(n-1))
}

// LinReg fits y = intercept + slope*x with x = 0,1,...,n-1 and returns the
// slope, intercept and coefficient of determination.
func LinReg(y []float64) (slope, intercept, r2 float64) {
	n := float64(len(y))
	if n < 2 {
		return 0, Mean(y), 0
	}
	// Closed-form sums for x = 0..n-1 avoid a second pass over x.
	sumX := n * (n - 1) / 2
	sumXX := (n - 1) * n * (2*n - 1) / 6
	var sumY, sumXY float64
	for i, v := range y {
		sumY += v
		sumXY += float64(i) * v
	}
	den := n*sumXX - sumX*sumX
	if den == 0 {
		return 0, sumY / n, 0
	}
	slope = (n*sumXY - sumX*sumY) / den
	intercept = (sumY - slope*sumX) / n

	meanY := sumY / n
	var ssTot, ssRes float64
	for i, v := range y {
		fit := intercept + slope*float64(i)
		ssRes += (v - fit) * (v - fit)
		ssTot += (v - meanY) * (v - meanY)
	}
	if ssTot == 0 {
		return slope, intercept, 0
	}
	return slope, intercept, 1 - ssRes/ssTot
}

// SMA returns the simple moving average of the last n values, or 0 if
// fewer than n values are available.
func SMA(xs []float64, n int) float64 {
	if n <= 0 || len(xs) < n {
		return 0
	}
	return Mean(xs[len(xs)-n:])
}

// RSI returns Wilder's relative strength index over period n for the most
// recent bar. It returns 50 when there is insufficient data.
func RSI(closes []float64, n int) float64 {
	if n <= 0 || len(closes) <= n {
		return 50
	}
	var gain, loss float64
	for i := 1; i <= n; i++ {
		d := closes[i] - closes[i-1]
		if d > 0 {
			gain += d
		} else {
			loss -= d
		}
	}
	avgGain, avgLoss := gain/float64(n), loss/float64(n)
	for i := n + 1; i < len(closes); i++ {
		d := closes[i] - closes[i-1]
		g, l := 0.0, 0.0
		if d > 0 {
			g = d
		} else {
			l = -d
		}
		avgGain = (avgGain*float64(n-1) + g) / float64(n)
		avgLoss = (avgLoss*float64(n-1) + l) / float64(n)
	}
	if avgLoss == 0 {
		return 100
	}
	rs := avgGain / avgLoss
	return 100 - 100/(1+rs)
}

// MaxDrawdown returns the largest peak-to-trough decline as a positive
// fraction (0.25 means a 25% drawdown).
func MaxDrawdown(closes []float64) float64 {
	var peak, maxDD float64
	for _, p := range closes {
		if p > peak {
			peak = p
		}
		if peak > 0 {
			if dd := 1 - p/peak; dd > maxDD {
				maxDD = dd
			}
		}
	}
	return maxDD
}

// TotalReturn returns the simple return over the last n bars, i.e. from
// closes[len-1-n] to the final close. It returns 0 when n is out of range.
func TotalReturn(closes []float64, n int) float64 {
	if n <= 0 || len(closes) <= n {
		return 0
	}
	start := closes[len(closes)-1-n]
	if start <= 0 {
		return 0
	}
	return closes[len(closes)-1]/start - 1
}

// NormCDF is the standard normal cumulative distribution function.
func NormCDF(x float64) float64 {
	return 0.5 * math.Erfc(-x/math.Sqrt2)
}
