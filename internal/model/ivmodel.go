package model

import (
	"math"

	"github.com/asrijanga/market-predictions/internal/options"
	"github.com/asrijanga/market-predictions/internal/quant"
)

// IVModel is the implied-volatility surface written as diffusive variance
// that accrues with time plus a discrete variance bump for each scheduled
// event (in practice, the earnings report) that falls before expiry:
//
//	IV(T)² * T = BaseVol² * T + JumpStdev² * 1{event before T}
//
// Fitting it to the listed term structure separates "the stock is volatile"
// from "the market is paying up for one binary date", which is what decides
// whether a call should be bought before or after that date. Across strikes
// the surface is taken as sticky in strike space with a linear skew.
type IVModel struct {
	BaseVol     float64 `json:"base_vol"`     // annualised diffusive volatility
	JumpStdev   float64 `json:"jump_stdev"`   // earnings-day return standard deviation
	SkewSlope   float64 `json:"skew_slope"`   // dIV / dln(K/S), negative for equities
	EarningsIdx int     `json:"earnings_idx"` // trading days from today, 0 if none
	Fitted      bool    `json:"fitted"`
}

// IVTerm is one expiry's at-the-money implied volatility.
type IVTerm struct {
	Days     int     // trading days to expiry
	IV       float64 // annualised at-the-money implied volatility
	HasEvent bool    // an earnings report falls on or before this expiry
}

// FitIVModel solves the variance decomposition by non-negative least
// squares on two regressors: elapsed time and the event indicator. With no
// variation in the indicator the event term is unidentified and is set to
// zero, leaving a pure diffusive fit.
func FitIVModel(terms []IVTerm, earningsIdx int) IVModel {
	m := IVModel{EarningsIdx: earningsIdx}
	var pts []IVTerm
	for _, t := range terms {
		if t.Days >= 3 && t.IV > 0 {
			pts = append(pts, t)
		}
	}
	if len(pts) == 0 {
		return m
	}
	// y = total variance to expiry; x1 = years; x2 = event indicator.
	var s11, s12, s22, sy1, sy2 float64
	var withEvent, without int
	for _, t := range pts {
		x1 := float64(t.Days) / quant.TradingDaysPerYear
		x2 := 0.0
		if t.HasEvent {
			x2, withEvent = 1, withEvent+1
		} else {
			without++
		}
		y := t.IV * t.IV * x1
		s11 += x1 * x1
		s12 += x1 * x2
		s22 += x2 * x2
		sy1 += x1 * y
		sy2 += x2 * y
	}
	var b1, b2 float64
	det := s11*s22 - s12*s12
	if withEvent > 0 && without > 0 && math.Abs(det) > 1e-18 {
		b1 = (sy1*s22 - sy2*s12) / det
		b2 = (sy2*s11 - sy1*s12) / det
	}
	if b1 <= 0 || b2 < 0 || (withEvent == 0 || without == 0) {
		// Fall back to a diffusive-only fit through the origin.
		b1, b2 = sy1/math.Max(s11, 1e-18), 0
	}
	if b1 <= 0 {
		return m
	}
	m.BaseVol = math.Sqrt(b1)
	m.JumpStdev = math.Sqrt(math.Max(b2, 0))
	m.Fitted = true
	return m
}

// ATM returns the model's at-the-money implied volatility for an option with
// days trading days left, given whether the event is still ahead of it.
func (m IVModel) ATM(days int, eventAhead bool) float64 {
	if days <= 0 || m.BaseVol <= 0 {
		return m.BaseVol
	}
	years := float64(days) / quant.TradingDaysPerYear
	v := m.BaseVol * m.BaseVol * years
	if eventAhead {
		v += m.JumpStdev * m.JumpStdev
	}
	return clampVol(math.Sqrt(v / years))
}

// Quote returns the implied volatility to price a strike, applying the skew
// in log-moneyness against the spot the surface was fitted at.
func (m IVModel) Quote(strike, fitSpot float64, days int, eventAhead bool) float64 {
	iv := m.ATM(days, eventAhead)
	if m.SkewSlope != 0 && strike > 0 && fitSpot > 0 {
		iv += m.SkewSlope * math.Log(strike/fitSpot)
	}
	return clampVol(iv)
}

// CrushPct is the fraction of at-the-money implied volatility that the model
// expects to disappear the moment the event passes, for an option with days
// left at that point.
func (m IVModel) CrushPct(days int) float64 {
	before := m.ATM(days, true)
	if before <= 0 {
		return 0
	}
	return 1 - m.ATM(days, false)/before
}

func clampVol(v float64) float64 { return math.Max(0.05, math.Min(3.0, v)) }

// FitSkew regresses implied volatility on log-moneyness across the strikes
// of one expiry, giving the slope used to price strikes away from the money.
func FitSkew(calls []options.StrikeQuote, spot float64) float64 {
	var xs, ys []float64
	for _, c := range calls {
		if c.IV > 0 && c.Strike > 0 {
			xs = append(xs, math.Log(c.Strike/spot))
			ys = append(ys, c.IV)
		}
	}
	if len(xs) < 3 {
		return 0
	}
	mx, my := quant.Mean(xs), quant.Mean(ys)
	var sxy, sxx float64
	for i := range xs {
		sxy += (xs[i] - mx) * (ys[i] - my)
		sxx += (xs[i] - mx) * (xs[i] - mx)
	}
	if sxx <= 1e-12 {
		return 0
	}
	// Guard against a runaway slope from a few illiquid strikes.
	return math.Max(-2, math.Min(2, sxy/sxx))
}
