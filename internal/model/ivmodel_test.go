package model

import (
	"math"
	"testing"

	"github.com/asrijanga/market-predictions/internal/options"
	"github.com/asrijanga/market-predictions/internal/quant"
)

func TestFitIVModelRecoversBaseAndJump(t *testing.T) {
	baseVol, jump, earnings := 0.26, 0.05, 30
	var terms []IVTerm
	for _, days := range []int{10, 21, 26, 46, 66, 86} {
		years := float64(days) / quant.TradingDaysPerYear
		v := baseVol * baseVol * years
		hasEvent := days >= earnings
		if hasEvent {
			v += jump * jump
		}
		terms = append(terms, IVTerm{Days: days, IV: math.Sqrt(v / years), HasEvent: hasEvent})
	}
	m := FitIVModel(terms, earnings)
	if !m.Fitted {
		t.Fatal("model should fit")
	}
	if math.Abs(m.BaseVol-baseVol) > 0.005 {
		t.Errorf("base vol = %.4f, want %.4f", m.BaseVol, baseVol)
	}
	if math.Abs(m.JumpStdev-jump) > 0.005 {
		t.Errorf("jump = %.4f, want %.4f", m.JumpStdev, jump)
	}
	// The event premium must show up as a higher quote before the report
	// than after it, for the same remaining life.
	before, after := m.ATM(21, true), m.ATM(21, false)
	if before <= after {
		t.Errorf("pre-event IV %.3f should exceed post-event %.3f", before, after)
	}
	if crush := m.CrushPct(21); crush <= 0 || crush > 0.5 {
		t.Errorf("crush = %v", crush)
	}
}

func TestFitIVModelWithoutEventVariation(t *testing.T) {
	terms := []IVTerm{{Days: 21, IV: 0.3}, {Days: 46, IV: 0.3}, {Days: 86, IV: 0.3}}
	m := FitIVModel(terms, 0)
	if !m.Fitted || math.Abs(m.BaseVol-0.3) > 0.01 || m.JumpStdev != 0 {
		t.Fatalf("model = %+v", m)
	}
}

func TestQuoteAppliesSkew(t *testing.T) {
	m := IVModel{BaseVol: 0.3, SkewSlope: -0.2, Fitted: true}
	low := m.Quote(300, 330, 46, false)
	high := m.Quote(360, 330, 46, false)
	if low <= high {
		t.Errorf("negative skew should price low strikes richer: %.3f vs %.3f", low, high)
	}
	if v := m.Quote(330, 330, 46, false); math.Abs(v-0.3) > 1e-9 {
		t.Errorf("at-the-money quote = %v, want the base vol", v)
	}
}

func TestFitSkewRecoversSlope(t *testing.T) {
	spot, slope, base := 330.0, -0.15, 0.28
	var calls []options.StrikeQuote
	for _, k := range []float64{320, 330, 340, 350, 360} {
		calls = append(calls, options.StrikeQuote{Strike: k, IV: base + slope*math.Log(k/spot)})
	}
	if got := FitSkew(calls, spot); math.Abs(got-slope) > 1e-6 {
		t.Errorf("skew = %v, want %v", got, slope)
	}
	if got := FitSkew(nil, spot); got != 0 {
		t.Errorf("empty chain should give no skew, got %v", got)
	}
}
