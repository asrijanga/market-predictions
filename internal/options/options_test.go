package options

import (
	"math"
	"testing"
	"time"

	"github.com/asrijanga/market-predictions/internal/market"
)

func TestBlackScholesKnownValue(t *testing.T) {
	// Hull textbook example: S=42, K=40, r=10%, vol=20%, T=0.5 -> call 4.76, put 0.81
	c := CallPrice(42, 40, 0.5, 0.10, 0.20)
	p := PutPrice(42, 40, 0.5, 0.10, 0.20)
	if math.Abs(c-4.76) > 0.01 || math.Abs(p-0.81) > 0.01 {
		t.Fatalf("call=%.3f put=%.3f", c, p)
	}
}

func TestImpliedVolRoundTrip(t *testing.T) {
	price := CallPrice(330, 335, 0.18, 0.04, 0.28)
	iv, ok := ImpliedVol(price, 330, 335, 0.18, 0.04, true)
	if !ok || math.Abs(iv-0.28) > 1e-3 {
		t.Fatalf("iv=%v ok=%v", iv, ok)
	}
	if _, ok := ImpliedVol(0.001, 330, 200, 0.18, 0.04, true); ok {
		t.Error("price below intrinsic should not solve")
	}
}

func TestSummarize(t *testing.T) {
	now := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	exp := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	years := exp.Sub(now).Hours() / (24 * 365)
	var qs []market.OptionQuote
	for _, k := range []float64{300, 320, 330, 335, 340, 350, 360, 380} {
		c := CallPrice(332, k, years, 0.04, 0.30)
		p := PutPrice(332, k, years, 0.04, 0.30)
		qs = append(qs, market.OptionQuote{Expiry: exp, Strike: k,
			Call: market.OptionSide{Bid: c - 0.1, Ask: c + 0.1, OpenInterest: 100},
			Put:  market.OptionSide{Bid: p - 0.1, Ask: p + 0.1, OpenInterest: 200}})
	}
	got := Summarize(qs, 332, now, 0.04)
	if len(got) != 1 {
		t.Fatalf("got %d expiries", len(got))
	}
	s := got[0]
	if !s.Monthly || s.ATMStrike != 330 || math.Abs(s.ATMCallIV-0.30) > 0.01 {
		t.Errorf("summary = %+v", s)
	}
	if s.PutCallOIRatio != 2 {
		t.Errorf("put/call OI = %v", s.PutCallOIRatio)
	}
	// 330..360 fall within -3%..+12% of 332; 300/320/380 do not.
	if len(s.Calls) != 5 || s.Calls[0].Strike != 330 || s.Calls[4].Strike != 360 {
		t.Errorf("calls = %+v", s.Calls)
	}
	if s.Calls[1].Delta <= s.Calls[4].Delta {
		t.Error("delta should fall as strike rises")
	}
}

func TestRealizedVolPercentile(t *testing.T) {
	closes := make([]float64, 300)
	p := 100.0
	for i := range closes {
		amp := 0.005
		if i > 280 {
			amp = 0.05 // volatile finish
		}
		if i%2 == 0 {
			p *= 1 + amp
		} else {
			p /= 1 + amp
		}
		closes[i] = p
	}
	if pct := RealizedVolPercentile(closes, 20); pct < 0.9 {
		t.Fatalf("expected top-decile percentile, got %v", pct)
	}
}
