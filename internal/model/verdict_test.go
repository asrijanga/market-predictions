package model

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestStanceThresholds(t *testing.T) {
	cases := map[float64]string{
		0.9: StanceStrongBull, 0.3: StanceBull, 0: StanceNeutral,
		-0.3: StanceBear, -0.9: StanceStrongBear,
	}
	for score, want := range cases {
		if got := Stance(score); got != want {
			t.Errorf("Stance(%.1f) = %q, want %q", score, got, want)
		}
	}
}

func TestSignalDriftIsBounded(t *testing.T) {
	if got := SignalDrift(1, 0.04, 0.15); math.Abs(got-0.19) > 1e-9 {
		t.Errorf("max bullish drift = %v, want 0.19", got)
	}
	if got := SignalDrift(-1, 0.04, 0.15); math.Abs(got+0.11) > 1e-9 {
		t.Errorf("max bearish drift = %v, want -0.11", got)
	}
	if got := SignalDrift(5, 0.04, 0.15); math.Abs(got-0.19) > 1e-9 {
		t.Errorf("a score beyond the scale must clamp, got %v", got)
	}
}

func TestSignalsSeparateBullAndBear(t *testing.T) {
	p := testPack(t)
	sigs, score := Signals(p, IVModel{SkewSlope: -0.15})
	if len(sigs) < 8 {
		t.Fatalf("got %d signals", len(sigs))
	}
	// Sorted by absolute contribution, strongest first.
	for i := 1; i < len(sigs); i++ {
		if math.Abs(sigs[i-1].Contribution) < math.Abs(sigs[i].Contribution) {
			t.Fatal("signals are not ordered by influence")
		}
	}
	for _, s := range sigs {
		if s.Score < -1 || s.Score > 1 {
			t.Errorf("signal %q scored %v, outside the scale", s.Name, s.Score)
		}
		if math.Abs(s.Contribution-s.Score*s.Weight) > 1e-12 {
			t.Errorf("signal %q contribution does not match score × weight", s.Name)
		}
	}
	if score < -1 || score > 1 {
		t.Errorf("composite %v outside the scale", score)
	}
	// The synthetic series rises throughout, so the read should be positive.
	if score <= 0 || Stance(score) == StanceBear {
		t.Errorf("a rising series should not read bearish: score %v", score)
	}
}

func TestBuildOutlookOrdersItsBand(t *testing.T) {
	paths := make([][]float64, 1000)
	for i := range paths {
		p := make([]float64, 64)
		p[0] = 1
		step := 1 + float64(i-500)/10000
		for d := 1; d < 64; d++ {
			p[d] = p[d-1] * step
		}
		paths[i] = p
	}
	sim := fixedSim(paths)
	now := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	o := BuildOutlook("Next quarter", 63, now, 100, sim, sim)
	if o.Name != "Next quarter" || o.Days != 63 {
		t.Fatalf("outlook = %+v", o)
	}
	if !(o.Low < o.Median && o.Median < o.High) {
		t.Errorf("band out of order: %+v", o)
	}
	if o.Direction != "up" && o.Direction != "down" {
		t.Errorf("direction = %q", o.Direction)
	}
	if o.Date.Weekday() == time.Saturday || o.Date.Weekday() == time.Sunday {
		t.Error("forecast date should be a weekday")
	}
}

func TestBuildOutlookBeyondHorizon(t *testing.T) {
	sim := fixedSim([][]float64{flat(11, 1.1)})
	o := BuildOutlook("Next year", 252, time.Now(), 100, sim, nil)
	if o.ProbUp != 0 || o.Median != 0 {
		t.Errorf("a horizon past the simulation should stay empty: %+v", o)
	}
}

func TestContributionTextNamesTheSide(t *testing.T) {
	if got := contributionText(Signal{Score: 0.9, Weight: 0.2, Contribution: 0.18}); !strings.Contains(got, "strongly bullish") {
		t.Errorf("got %q", got)
	}
	if got := contributionText(Signal{Score: -0.5, Weight: 0.1, Contribution: -0.05}); !strings.Contains(got, "moderately bearish") {
		t.Errorf("got %q", got)
	}
}

func TestOrdinal(t *testing.T) {
	cases := map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th",
		13: "13th", 21: "21st", 49: "49th", 71: "71st", 100: "100th", 0: "0th"}
	for n, want := range cases {
		if got := ordinal(n); got != want {
			t.Errorf("ordinal(%d) = %q, want %q", n, got, want)
		}
	}
}
