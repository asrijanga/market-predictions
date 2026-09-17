package model

import (
	"math"
	"math/rand/v2"
	"testing"
)

// syntheticGARCH generates returns from a known GARCH(1,1) process.
func syntheticGARCH(n int, alpha, beta, uncond float64, seed uint64) []float64 {
	rng := rand.New(rand.NewPCG(seed, 7))
	omega := uncond * (1 - alpha - beta)
	out := make([]float64, n)
	sigma2 := uncond
	for i := range out {
		e := math.Sqrt(sigma2) * rng.NormFloat64()
		out[i] = e
		sigma2 = omega + alpha*e*e + beta*sigma2
	}
	return out
}

func TestFitGARCHRecoversParameters(t *testing.T) {
	uncond := 0.0004 // ~32% annualised
	r := syntheticGARCH(4000, 0.10, 0.85, uncond, 42)
	g := FitGARCH(r)
	if math.Abs(g.Alpha-0.10) > 0.05 {
		t.Errorf("alpha = %.3f, want ~0.10", g.Alpha)
	}
	if math.Abs(g.Beta-0.85) > 0.08 {
		t.Errorf("beta = %.3f, want ~0.85", g.Beta)
	}
	if math.Abs(g.UncondVar-uncond)/uncond > 0.25 {
		t.Errorf("uncond var = %g, want ~%g", g.UncondVar, uncond)
	}
	if len(g.Resid) != len(r) {
		t.Fatalf("residuals length %d", len(g.Resid))
	}
	if sd := stddev(g.Resid); math.Abs(sd-1) > 0.15 {
		t.Errorf("standardized residual stdev = %.3f, want ~1", sd)
	}
	if g.Persistence() >= 1 {
		t.Errorf("persistence %.3f must be < 1", g.Persistence())
	}
	if hl := g.HalfLife(); hl < 1 || hl > 200 {
		t.Errorf("half life = %v", hl)
	}
}

func TestVarPathConvergesToUnconditional(t *testing.T) {
	g := GARCH{Alpha: 0.1, Beta: 0.85, UncondVar: 0.0004, Omega: 0.0004 * 0.05}
	path := g.VarPath(0.0016, 400) // start 4x the long-run level
	if path[0] <= path[10] || path[10] <= path[100] {
		t.Fatal("variance forecast should decay toward the unconditional level")
	}
	if math.Abs(path[399]-g.UncondVar)/g.UncondVar > 0.01 {
		t.Errorf("tail of path = %g, want ~%g", path[399], g.UncondVar)
	}
}

func TestScoreVolModelsBeatsConstantVarianceOnGARCHData(t *testing.T) {
	// Strong, persistent volatility clustering is exactly the structure a
	// constant-variance forecast cannot capture.
	r := syntheticGARCH(1500, 0.12, 0.86, 0.0004, 9)
	scores := ScoreVolModels(r, 400)
	if len(scores) != 4 {
		t.Fatalf("got %d scores", len(scores))
	}
	byName := map[string]VolScore{}
	for _, s := range scores {
		if s.QLIKE <= 0 || math.IsNaN(s.QLIKE) || s.RMSE <= 0 {
			t.Errorf("implausible score %+v", s)
		}
		byName[s.Name] = s
	}
	if byName["GARCH(1,1)"].QLIKE >= byName["Constant variance"].QLIKE {
		t.Errorf("GARCH QLIKE %.4f should beat constant variance %.4f on clustered data",
			byName["GARCH(1,1)"].QLIKE, byName["Constant variance"].QLIKE)
	}
}

func TestSimulateReproducesVolAndDrift(t *testing.T) {
	uncond := 0.0004
	r := syntheticGARCH(2000, 0.08, 0.88, uncond, 3)
	g := FitGARCH(r)
	cfg := SimConfig{Paths: 8000, Horizon: 63, DailyDrift: 0.0005, StartVar: g.UncondVar, Seed: 11}
	sim := Simulate(g, g.Resid, cfg)
	if sim.Paths != 8000 || sim.At(0, 0) != 1 {
		t.Fatal("simulation not initialised at 1")
	}
	// Mean log return should track the drift over the horizon.
	var sum float64
	for p := 0; p < sim.Paths; p++ {
		sum += math.Log(sim.At(p, 63))
	}
	mean := sum / float64(sim.Paths)
	want := 0.0005 * 63
	if math.Abs(mean-want) > 0.01 {
		t.Errorf("mean log return %.4f, want ~%.4f", mean, want)
	}
	annual := math.Sqrt(uncond * 252)
	if got := sim.RealizedVol(63); math.Abs(got-annual)/annual > 0.15 {
		t.Errorf("simulated vol %.3f, want ~%.3f", got, annual)
	}
	if lo, hi := sim.Quantile(63, 0.1), sim.Quantile(63, 0.9); lo >= hi || lo <= 0 {
		t.Errorf("quantiles out of order: %v %v", lo, hi)
	}
	if p := sim.ProbAbove(63, 1.0); p < 0.4 || p > 0.75 {
		t.Errorf("P(up) = %v, implausible", p)
	}
}

func TestSimulateIsDeterministicForASeed(t *testing.T) {
	g := FitGARCH(syntheticGARCH(600, 0.1, 0.85, 0.0003, 5))
	cfg := SimConfig{Paths: 500, Horizon: 20, StartVar: g.UncondVar, Seed: 99}
	a, b := Simulate(g, g.Resid, cfg), Simulate(g, g.Resid, cfg)
	for p := 0; p < 500; p++ {
		if a.At(p, 20) != b.At(p, 20) {
			t.Fatalf("path %d differs between runs", p)
		}
	}
}

func TestEarningsJumpWidensTheDistribution(t *testing.T) {
	g := FitGARCH(syntheticGARCH(1000, 0.08, 0.88, 0.0002, 4))
	base := SimConfig{Paths: 6000, Horizon: 40, StartVar: g.UncondVar, Seed: 21}
	withJump := base
	withJump.EarningsIdx, withJump.JumpStdev = 20, 0.06
	quiet := Simulate(g, g.Resid, base)
	jumpy := Simulate(g, g.Resid, withJump)
	if jumpy.RealizedVol(40) <= quiet.RealizedVol(40) {
		t.Fatal("earnings jump should raise total volatility")
	}
	if quiet.RealizedVol(19) == 0 || math.Abs(jumpy.RealizedVol(19)-quiet.RealizedVol(19))/quiet.RealizedVol(19) > 0.1 {
		t.Error("volatility before the jump day should be unchanged")
	}
}

func stddev(xs []float64) float64 {
	var m float64
	for _, x := range xs {
		m += x
	}
	m /= float64(len(xs))
	var ss float64
	for _, x := range xs {
		ss += (x - m) * (x - m)
	}
	return math.Sqrt(ss / float64(len(xs)-1))
}
