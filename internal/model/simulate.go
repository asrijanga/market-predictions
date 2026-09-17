package model

import (
	"math"
	"math/rand/v2"
	"runtime"
	"sort"
	"sync"
)

// SimConfig parameterises a filtered historical simulation: GARCH supplies
// the conditional variance dynamics, the standardized residuals of the
// fitted model supply the shape of the shocks (so fat tails and asymmetry
// in the stock's own history survive), and a separate draw represents the
// earnings jump on the one day it happens.
type SimConfig struct {
	Paths        int
	Horizon      int     // trading days to simulate
	DailyDrift   float64 // expected daily log return
	StartVar     float64 // one-step-ahead conditional variance
	EarningsDays []int   // day indices carrying an earnings jump
	JumpStdev    float64 // standard deviation of an earnings-day return
	Seed         uint64
}

// Sim holds simulated price paths as ratios to today's spot, indexed by day
// 0 (today, always 1) through Horizon.
type Sim struct {
	Paths   int
	Horizon int
	ratio   []float64 // flat [path*(Horizon+1) + day]
}

// At returns S_day / S_0 on one path.
func (s *Sim) At(path, day int) float64 { return s.ratio[path*(s.Horizon+1)+day] }

// Quantile returns the q-quantile of the price ratio on a given day.
func (s *Sim) Quantile(day int, q float64) float64 {
	col := make([]float64, s.Paths)
	for p := 0; p < s.Paths; p++ {
		col[p] = s.At(p, day)
	}
	sort.Float64s(col)
	i := int(q * float64(s.Paths-1))
	return col[max(0, min(s.Paths-1, i))]
}

// ProbAbove returns the fraction of paths above a price ratio on a day.
func (s *Sim) ProbAbove(day int, ratio float64) float64 {
	n := 0
	for p := 0; p < s.Paths; p++ {
		if s.At(p, day) > ratio {
			n++
		}
	}
	return float64(n) / float64(s.Paths)
}

// Simulate runs the filtered historical simulation. Work is split across
// CPUs, each with an independent random stream derived from Seed so that
// results are reproducible regardless of core count.
func Simulate(g GARCH, resid []float64, cfg SimConfig) *Sim {
	if cfg.Paths <= 0 || cfg.Horizon <= 0 || len(resid) == 0 {
		return &Sim{Paths: 0, Horizon: cfg.Horizon}
	}
	s := &Sim{Paths: cfg.Paths, Horizon: cfg.Horizon, ratio: make([]float64, cfg.Paths*(cfg.Horizon+1))}
	startVar := cfg.StartVar
	if startVar <= 0 {
		startVar = g.UncondVar
	}
	isEarnings := make([]bool, cfg.Horizon+1)
	for _, d := range cfg.EarningsDays {
		if d > 0 && d <= cfg.Horizon {
			isEarnings[d] = true
		}
	}
	workers := min(runtime.NumCPU(), cfg.Paths)
	var wg sync.WaitGroup
	chunk := (cfg.Paths + workers - 1) / workers
	for w := 0; w < workers; w++ {
		lo := w * chunk
		hi := min(lo+chunk, cfg.Paths)
		if lo >= hi {
			continue
		}
		wg.Add(1)
		go func(lo, hi, w int) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(cfg.Seed, uint64(w)+1))
			for p := lo; p < hi; p++ {
				base := p * (cfg.Horizon + 1)
				s.ratio[base] = 1
				logPrice := 0.0
				sigma2 := startVar
				for d := 1; d <= cfg.Horizon; d++ {
					z := resid[rng.IntN(len(resid))]
					shock := math.Sqrt(sigma2) * z
					r := cfg.DailyDrift + shock
					if isEarnings[d] && cfg.JumpStdev > 0 {
						r += cfg.JumpStdev * rng.NormFloat64()
					}
					logPrice += r
					s.ratio[base+d] = math.Exp(logPrice)
					sigma2 = g.Omega + g.Alpha*shock*shock + g.Beta*sigma2
					if sigma2 <= 0 || math.IsNaN(sigma2) {
						sigma2 = g.UncondVar
					}
				}
			}
		}(lo, hi, w)
	}
	wg.Wait()
	return s
}

// RealizedVol returns the annualised volatility of the simulated paths over
// the first h days, a check that the simulation reproduces the model's own
// variance forecast.
func (s *Sim) RealizedVol(h int) float64 {
	if s.Paths == 0 || h <= 0 || h > s.Horizon {
		return 0
	}
	var sum, sumSq float64
	for p := 0; p < s.Paths; p++ {
		r := math.Log(s.At(p, h))
		sum += r
		sumSq += r * r
	}
	n := float64(s.Paths)
	variance := (sumSq - sum*sum/n) / (n - 1)
	return math.Sqrt(variance / float64(h) * 252)
}
