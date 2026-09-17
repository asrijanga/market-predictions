// Package options prices listed options with Black-Scholes and summarises
// an option chain the way a trader would read it.
package options

import (
	"math"
	"sort"
	"time"

	"github.com/asrijanga/market-predictions/internal/market"
	"github.com/asrijanga/market-predictions/internal/quant"
)

// CallPrice returns the Black-Scholes price of a European call.
func CallPrice(spot, strike, years, rate, vol float64) float64 {
	if years <= 0 || vol <= 0 {
		return math.Max(spot-strike, 0)
	}
	d1 := (math.Log(spot/strike) + (rate+vol*vol/2)*years) / (vol * math.Sqrt(years))
	d2 := d1 - vol*math.Sqrt(years)
	return spot*quant.NormCDF(d1) - strike*math.Exp(-rate*years)*quant.NormCDF(d2)
}

// PutPrice returns the Black-Scholes price of a European put via parity.
func PutPrice(spot, strike, years, rate, vol float64) float64 {
	return CallPrice(spot, strike, years, rate, vol) - spot + strike*math.Exp(-rate*years)
}

// CallDelta returns the Black-Scholes delta of a call.
func CallDelta(spot, strike, years, rate, vol float64) float64 {
	if years <= 0 || vol <= 0 {
		if spot > strike {
			return 1
		}
		return 0
	}
	d1 := (math.Log(spot/strike) + (rate+vol*vol/2)*years) / (vol * math.Sqrt(years))
	return quant.NormCDF(d1)
}

// ImpliedVol solves for the annualised volatility that reproduces price.
// It returns ok=false when the price is outside no-arbitrage bounds.
func ImpliedVol(price, spot, strike, years, rate float64, call bool) (iv float64, ok bool) {
	if price <= 0 || spot <= 0 || strike <= 0 || years <= 0 {
		return 0, false
	}
	model := func(v float64) float64 {
		if call {
			return CallPrice(spot, strike, years, rate, v)
		}
		return PutPrice(spot, strike, years, rate, v)
	}
	lo, hi := 0.01, 5.0
	if price <= model(lo) || price >= model(hi) {
		return 0, false
	}
	for i := 0; i < 100; i++ {
		mid := (lo + hi) / 2
		if model(mid) < price {
			lo = mid
		} else {
			hi = mid
		}
		if hi-lo < 1e-5 {
			break
		}
	}
	return (lo + hi) / 2, true
}

// StrikeQuote is one call strike with derived analytics.
type StrikeQuote struct {
	Strike       float64 `json:"strike"`
	Moneyness    float64 `json:"moneyness"` // strike/spot - 1
	Mid          float64 `json:"mid"`
	Bid          float64 `json:"bid"`
	Ask          float64 `json:"ask"`
	SpreadPct    float64 `json:"spread_pct"` // (ask-bid)/mid
	IV           float64 `json:"iv"`
	Delta        float64 `json:"delta"`
	Breakeven    float64 `json:"breakeven"`
	BreakevenPct float64 `json:"breakeven_pct"`
	OpenInterest int64   `json:"open_interest"`
	Volume       int64   `json:"volume"`
}

// ExpirySummary condenses one expiry of the chain.
type ExpirySummary struct {
	Expiry          time.Time     `json:"expiry"`
	Days            int           `json:"days"`
	Monthly         bool          `json:"monthly"`
	ATMStrike       float64       `json:"atm_strike"`
	ATMCallIV       float64       `json:"atm_call_iv"`
	ATMPutIV        float64       `json:"atm_put_iv"`
	StraddleMovePct float64       `json:"straddle_move_pct"` // ATM straddle / spot
	CallOI          int64         `json:"call_oi"`
	PutOI           int64         `json:"put_oi"`
	PutCallOIRatio  float64       `json:"put_call_oi_ratio"`
	Calls           []StrikeQuote `json:"calls"` // ATM to ~10% OTM
}

// Summarize derives per-expiry analytics from raw quotes. Strikes listed
// under Calls run from just below spot to roughly 10% above it.
func Summarize(quotes []market.OptionQuote, spot float64, now time.Time, rate float64) []ExpirySummary {
	byExpiry := map[time.Time][]market.OptionQuote{}
	for _, q := range quotes {
		byExpiry[q.Expiry] = append(byExpiry[q.Expiry], q)
	}
	var out []ExpirySummary
	for exp, qs := range byExpiry {
		days := quant.TradingDays(now, exp)
		if days <= 0 {
			continue
		}
		years := float64(exp.Sub(now).Hours()) / (24 * 365)
		sort.Slice(qs, func(i, j int) bool { return qs[i].Strike < qs[j].Strike })

		s := ExpirySummary{Expiry: exp, Days: days, Monthly: exp.Equal(quant.ThirdFriday(exp.Year(), exp.Month()))}
		atmIdx := 0
		for i, q := range qs {
			s.CallOI += q.Call.OpenInterest
			s.PutOI += q.Put.OpenInterest
			if math.Abs(q.Strike-spot) < math.Abs(qs[atmIdx].Strike-spot) {
				atmIdx = i
			}
		}
		if s.PutOI > 0 && s.CallOI > 0 {
			s.PutCallOIRatio = float64(s.PutOI) / float64(s.CallOI)
		}
		atm := qs[atmIdx]
		s.ATMStrike = atm.Strike
		if iv, ok := ImpliedVol(atm.Call.Mid(), spot, atm.Strike, years, rate, true); ok {
			s.ATMCallIV = iv
		}
		if iv, ok := ImpliedVol(atm.Put.Mid(), spot, atm.Strike, years, rate, false); ok {
			s.ATMPutIV = iv
		}
		if straddle := atm.Call.Mid() + atm.Put.Mid(); straddle > 0 && spot > 0 {
			s.StraddleMovePct = straddle / spot
		}
		for _, q := range qs {
			m := q.Strike/spot - 1
			if m < -0.03 || m > 0.12 {
				continue
			}
			mid := q.Call.Mid()
			sq := StrikeQuote{
				Strike: q.Strike, Moneyness: m, Mid: mid, Bid: q.Call.Bid, Ask: q.Call.Ask,
				OpenInterest: q.Call.OpenInterest, Volume: q.Call.Volume,
			}
			if mid > 0 {
				sq.SpreadPct = (q.Call.Ask - q.Call.Bid) / mid
				sq.Breakeven = q.Strike + mid
				sq.BreakevenPct = sq.Breakeven/spot - 1
				if iv, ok := ImpliedVol(mid, spot, q.Strike, years, rate, true); ok {
					sq.IV = iv
					sq.Delta = CallDelta(spot, q.Strike, years, rate, iv)
				}
			}
			s.Calls = append(s.Calls, sq)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Expiry.Before(out[j].Expiry) })
	return out
}

// RealizedVol returns annualised close-to-close volatility of the last n bars.
func RealizedVol(closes []float64, n int) float64 {
	if len(closes) <= n {
		n = len(closes) - 1
	}
	if n < 2 {
		return 0
	}
	return quant.StdDev(quant.LogReturns(closes[len(closes)-n-1:])) * math.Sqrt(quant.TradingDaysPerYear)
}

// RealizedVolPercentile reports where the current n-day realised vol sits
// within the distribution of rolling n-day vols over the whole series
// (0 = lowest of the year, 1 = highest).
func RealizedVolPercentile(closes []float64, n int) float64 {
	if len(closes) < 2*n {
		return 0.5
	}
	rets := quant.LogReturns(closes)
	var rolling []float64
	for i := n; i <= len(rets); i++ {
		rolling = append(rolling, quant.StdDev(rets[i-n:i]))
	}
	current := rolling[len(rolling)-1]
	below := 0
	for _, v := range rolling {
		if v < current {
			below++
		}
	}
	return float64(below) / float64(len(rolling))
}
