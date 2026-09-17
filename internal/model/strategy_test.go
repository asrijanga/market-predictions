package model

import (
	"math"
	"testing"
	"time"
)

// fixedSim builds a Sim from explicit price-ratio paths so strategy
// arithmetic can be checked without randomness.
func fixedSim(paths [][]float64) *Sim {
	h := len(paths[0]) - 1
	s := &Sim{Paths: len(paths), Horizon: h, ratio: make([]float64, len(paths)*(h+1))}
	for i, p := range paths {
		copy(s.ratio[i*(h+1):], p)
	}
	return s
}

func flat(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	out[0] = 1
	return out
}

func testContract(expiryIdx int, strike float64) Contract {
	return Contract{
		Expiry: time.Date(2026, 12, 18, 0, 0, 0, 0, time.UTC), ExpiryIdx: expiryIdx,
		Strike: strike, MarketMid: 10, SpreadPct: 0.02, OpenInterest: 5000, Delta: 0.5,
	}
}

func TestEvaluateImmediateAlwaysFills(t *testing.T) {
	sim := fixedSim([][]float64{flat(41, 1.2), flat(41, 0.8)})
	c := testContract(40, 330)
	iv := make([]float64, 41)
	for i := range iv {
		iv[i] = 0.3
	}
	w := Window{StartIdx: 0, EndIdx: 0}
	st := Evaluate(sim, 330, c, w, Trigger{Kind: TriggerImmediate}, iv, 0.04)
	if st.PTrade != 1 {
		t.Fatalf("immediate entry should always fill, got %v", st.PTrade)
	}
	// One path finishes at 396 (payoff 66), the other at 264 (payoff 0).
	if st.PProfit != 0.5 || st.MeanPayoff != 33 {
		t.Errorf("stats = %+v", st)
	}
}

func TestEvaluateDipTriggerFillsOnlyOnDips(t *testing.T) {
	dips := flat(41, 1.0)
	for i := 5; i <= 10; i++ {
		dips[i] = 0.94
	}
	never := flat(41, 1.05)
	sim := fixedSim([][]float64{dips, never})
	c := testContract(40, 330)
	iv := make([]float64, 41)
	for i := range iv {
		iv[i] = 0.3
	}
	w := Window{StartIdx: 0, EndIdx: 12}
	st := Evaluate(sim, 330, c, w, Trigger{Kind: TriggerDip, Level: 0.95}, iv, 0.04)
	if math.Abs(st.PTrade-0.5) > 1e-9 {
		t.Fatalf("P(trade) = %v, want 0.5", st.PTrade)
	}
	if math.Abs(st.MeanEntry-330*0.94) > 1e-6 {
		t.Errorf("entry spot = %v, want the dip price", st.MeanEntry)
	}
}

func TestEvaluateRespectsRunway(t *testing.T) {
	sim := fixedSim([][]float64{flat(41, 1.2)})
	c := testContract(40, 330)
	iv := make([]float64, 41)
	for i := range iv {
		iv[i] = 0.3
	}
	// A window that opens inside the runway cannot produce a trade.
	late := Window{StartIdx: 40 - minRunway + 1, EndIdx: 40}
	if st := Evaluate(sim, 330, c, late, Trigger{Kind: TriggerImmediate}, iv, 0.04); st.PTrade != 0 {
		t.Fatalf("expected no trade inside the runway, got %+v", st)
	}
}

func TestGrowthRatePrefersBalancedOverLottery(t *testing.T) {
	lottery := make([]float64, 100)
	for i := range lottery {
		lottery[i] = -1
	}
	lottery[0], lottery[1], lottery[2] = 50, 50, 50 // high mean, 3% win rate
	balanced := make([]float64, 100)
	for i := range balanced {
		if i < 45 {
			balanced[i] = 1.0
		} else {
			balanced[i] = -0.6
		}
	}
	meanOf := func(xs []float64) float64 {
		var s float64
		for _, x := range xs {
			s += x
		}
		return s / float64(len(xs))
	}
	if meanOf(lottery) <= meanOf(balanced) {
		t.Fatal("test setup: the lottery should have the higher mean return")
	}
	if growthRate(lottery, RankingStake) >= growthRate(balanced, RankingStake) {
		t.Errorf("growth ranking should prefer the balanced payoff: lottery %.4f vs balanced %.4f",
			growthRate(lottery, RankingStake), growthRate(balanced, RankingStake))
	}
}

func TestKellyFraction(t *testing.T) {
	// A 60/40 double-or-nothing bet has a Kelly stake of p-q = 0.20.
	returns := make([]float64, 1000)
	for i := range returns {
		if i < 600 {
			returns[i] = 1
		} else {
			returns[i] = -1
		}
	}
	if f := KellyFraction(returns); math.Abs(f-0.20) > 0.02 {
		t.Errorf("Kelly fraction = %v, want ~0.20", f)
	}
	losing := make([]float64, 100)
	for i := range losing {
		if i < 40 {
			losing[i] = 1
		} else {
			losing[i] = -1
		}
	}
	if f := KellyFraction(losing); f != 0 {
		t.Errorf("a losing bet should get no stake, got %v", f)
	}
}

func TestTradingDayDateSkipsWeekends(t *testing.T) {
	fri := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	if got := TradingDayDate(fri, 1); got.Format(time.DateOnly) != "2026-09-21" {
		t.Errorf("one day after Friday = %s, want Monday", got.Format(time.DateOnly))
	}
	if got := TradingDayDate(fri, 5); got.Format(time.DateOnly) != "2026-09-25" {
		t.Errorf("five trading days after Friday = %s", got.Format(time.DateOnly))
	}
}

func TestBuildWindowsCoversEarnings(t *testing.T) {
	now := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	ws := BuildWindows(now, 63, 30)
	var hasAfter, hasBefore, hasToday bool
	for _, w := range ws {
		if w.StartIdx == 31 && w.Label == "after earnings" {
			hasAfter = true
		}
		if w.EndIdx == 29 && w.Label == "before earnings" {
			hasBefore = true
		}
		if w.StartIdx == 0 && w.EndIdx == 0 {
			hasToday = true
		}
		if w.EndIdx > 63 || w.StartIdx > w.EndIdx {
			t.Fatalf("malformed window %+v", w)
		}
	}
	if !hasAfter || !hasBefore || !hasToday {
		t.Errorf("missing anchored windows: today=%v before=%v after=%v", hasToday, hasBefore, hasAfter)
	}
}

func TestTopDistinctOnePlanPerTrigger(t *testing.T) {
	mk := func(kind string, level, growth float64) Strategy {
		return Strategy{
			Trigger: Trigger{Kind: kind, Level: level},
			Stats:   Stats{PTrade: 0.5, GrowthRate: growth},
		}
	}
	all := []Strategy{
		mk(TriggerDip, 0.95, 0.03),
		mk(TriggerDip, 0.95, 0.02), // same trigger, worse
		mk(TriggerImmediate, 0, 0.01),
		{Trigger: Trigger{Kind: TriggerBreakout, Level: 1.02}, Stats: Stats{PTrade: 0.01, GrowthRate: 0.09}},
	}
	got := TopDistinct(all, 5, 0.25)
	if len(got) != 2 {
		t.Fatalf("got %d plans, want 2 (one per trigger, the rare one filtered)", len(got))
	}
	if got[0].Stats.GrowthRate != 0.03 || got[1].Trigger.Kind != TriggerImmediate {
		t.Errorf("unexpected selection: %+v", got)
	}
}

func TestEvaluateRefusesAContractPastTheSimulation(t *testing.T) {
	// Sensitivity runs are simulated only as far as the furthest expiry, so
	// a contract beyond that must yield no trade instead of reading off the
	// end of the paths.
	sim := fixedSim([][]float64{flat(41, 1.2), flat(41, 0.9)})
	iv := make([]float64, 60)
	for i := range iv {
		iv[i] = 0.3
	}
	beyond := testContract(55, 330) // expires 15 days past the simulation
	st := Evaluate(sim, 330, beyond, Window{StartIdx: 0, EndIdx: 5}, Trigger{Kind: TriggerImmediate}, iv, 0.04)
	if st.PTrade != 0 || st.MeanReturn != 0 {
		t.Fatalf("expected no trade, got %+v", st)
	}
}
