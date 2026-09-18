// Package valuation estimates what a share is worth from what the company
// reports, and how long the market has historically taken to agree.
//
// Three models run, and they are blended rather than chosen between.
// Penman and Sougiannis (1998) and Francis, Olsson and Oswald (2000) both
// found residual income more accurate than discounted cash flow or
// dividend discounting over the finite horizons anyone actually forecasts
// over, because it is anchored on book value: the further the forecast
// reaches, the less of the answer it carries. It therefore takes the
// largest weight here. Multiples do well on percentage error and cost
// nothing to compute, so they come second, and an explicit earnings
// discount third.
package valuation

import (
	"math"
	"slices"

	"github.com/asrijanga/market-predictions/internal/market"
)

// EquityRiskPremium is the excess return equities are assumed to earn over
// the risk-free rate. Long-run realised premia cluster between four and
// six percent depending on the window and the averaging; five is a
// defensible middle and the answer is not sensitive to it at the margin.
const EquityRiskPremium = 0.05

// Horizon is how many years the explicit forecast runs before a terminal
// value takes over. Consensus estimates rarely reach further.
const Horizon = 5

// Inputs is everything a valuation needs that is not in the filings.
type Inputs struct {
	Price     float64
	Beta      float64 // against the benchmark; 1 when unknown
	RiskFree  float64
	GrowthCap float64 // ceiling on extrapolated growth
	Perpetual float64 // growth after the explicit horizon
}

// Defaults are conservative: a company cannot outgrow the economy forever,
// and a forecast growth rate above the cap is treated as fade rather than
// as a permanent condition.
func Defaults(riskFree float64) Inputs {
	return Inputs{
		Beta: 1, RiskFree: riskFree,
		GrowthCap: 0.15, Perpetual: 0.025,
	}
}

// Estimate is one model's answer.
type Estimate struct {
	Method string  `json:"method"`
	Value  float64 `json:"value"`
	Weight float64 `json:"weight"`
	Note   string  `json:"note"`
}

// Result is the blended fair value and the working behind it.
type Result struct {
	FairValue    float64    `json:"fair_value"`
	Upside       float64    `json:"upside"`        // fraction, fair/price - 1
	DiscountRate float64    `json:"discount_rate"` // CAPM cost of equity
	Growth       float64    `json:"growth"`        // implied by the forecasts
	TrailingPE   float64    `json:"trailing_pe"`
	ForwardPE    float64    `json:"forward_pe"`
	Estimates    []Estimate `json:"estimates"`
	// Confidence is how far apart the models landed: high, medium or low.
	Confidence string   `json:"confidence"`
	Spread     float64  `json:"spread"`
	BookTrust  float64  `json:"book_trust"`
	Warnings   []string `json:"warnings,omitempty"`
}

// CostOfEquity is CAPM: the risk-free rate plus the premium the market
// charges for this company's share of undiversifiable risk.
func CostOfEquity(riskFree, beta float64) float64 {
	if beta <= 0 {
		beta = 1
	}
	// A discount rate below the risk-free rate, or absurdly above it, says
	// the beta estimate is noise rather than information.
	return clamp(riskFree+beta*EquityRiskPremium, riskFree+0.02, riskFree+0.12)
}

// Growth reads the expected earnings growth out of the consensus, falling
// back to what the company has actually done. A forecast of explosive
// growth is capped: it is a forecast, and the long ones are rarely met.
func Growth(f market.Fundamentals, in Inputs) (float64, string) {
	if n := len(f.ForwardEPS); n >= 2 && f.ForwardEPS[0] > 0 && f.ForwardEPS[n-1] > 0 {
		years := float64(n - 1)
		g := math.Pow(f.ForwardEPS[n-1]/f.ForwardEPS[0], 1/years) - 1
		return clamp(g, -0.20, in.GrowthCap), "consensus"
	}
	if len(f.NetIncome) >= 2 {
		old, recent := f.NetIncome[len(f.NetIncome)-1], f.NetIncome[0]
		if old > 0 && recent > 0 {
			years := float64(len(f.NetIncome) - 1)
			g := math.Pow(recent/old, 1/years) - 1
			return clamp(g, -0.20, in.GrowthCap), "reported"
		}
	}
	return in.Perpetual, "assumed"
}

// ResidualIncome is the Edwards-Bell-Ohlson model: a share is worth its
// book value plus whatever the company earns above the cost of that
// capital, discounted.
//
//	V = B0 + Σ (ROE_t − r)·B_{t−1} / (1+r)^t
//
// Book value carries most of the answer, so an error in the forecast costs
// less here than in a model where every dollar of value is forecast. That
// is the whole reason it survives truncation better.
func ResidualIncome(f market.Fundamentals, in Inputs, r, g float64) (float64, bool) {
	b := f.BookValuePerShare
	if b <= 0 {
		return 0, false
	}
	eps := forwardOrTrailing(f)
	if eps == 0 {
		return 0, false
	}
	roe := eps / b
	if roe <= 0 {
		return 0, false
	}
	// Competition pulls extraordinary returns back toward the cost of
	// capital. Fading ROE rather than holding it is what keeps the terminal
	// value from doing all the work.
	const fade = 0.85
	payout := sustainablePayout(f, eps, g)

	value, book := b, b
	for t := 1; t <= Horizon; t++ {
		earnings := roe * book
		value += (earnings - r*book) / math.Pow(1+r, float64(t))
		book += earnings * (1 - payout)
		roe = r + (roe-r)*fade
	}
	// Terminal residual income, fading to nothing in perpetuity.
	terminal := (roe - r) * book / (r + (1 - fade))
	value += terminal / math.Pow(1+r, Horizon)
	return value, value > 0
}

// EarningsDiscount values the consensus earnings stream directly and adds
// a Gordon terminal value. It is the most forecast-dependent of the three,
// which is why it carries the least weight.
func EarningsDiscount(f market.Fundamentals, in Inputs, r, g float64) (float64, bool) {
	eps := forwardOrTrailing(f)
	if eps <= 0 || r <= in.Perpetual {
		return 0, false
	}
	value := 0.0
	e := eps
	for t := 1; t <= Horizon; t++ {
		if t-1 < len(f.ForwardEPS) && f.ForwardEPS[t-1] > 0 {
			e = f.ForwardEPS[t-1] // use the consensus while it reaches
		} else {
			e *= 1 + g
		}
		value += e / math.Pow(1+r, float64(t))
	}
	terminal := e * (1 + in.Perpetual) / (r - in.Perpetual)
	value += terminal / math.Pow(1+r, Horizon)
	return value, value > 0
}

// HistoricalMultiple prices the shares at the multiple they have actually
// traded on, rather than at one derived from assumptions.
//
// This replaced a Gordon-derived justified P/E, which is numerically
// unstable exactly where it is most needed: the formula divides by (r − g),
// so for any company growing near its cost of capital the denominator
// collapses and the answer runs away. Growth companies are precisely the
// ones it silently refused to value. The median multiple a company has
// traded on carries no such assumption, and it is the same premise as the
// reversion model: multiples return to their own level.
func HistoricalMultiple(f market.Fundamentals, peHistory []float64) (float64, bool) {
	eps := f.TrailingEPS
	if eps <= 0 || len(peHistory) < 120 {
		return 0, false
	}
	xs := append([]float64(nil), peHistory...)
	slices.Sort(xs)
	median := xs[len(xs)/2]
	if median <= 0 || median > 80 {
		return 0, false
	}
	return median * eps, true
}

// bookTrust says how much of the answer residual income deserves.
//
// The model is anchored on book value, and that anchor fails when book
// value has stopped describing the business: years of buybacks and
// expensed intangibles leave companies whose shareholders' equity is a
// rounding error against their market value. Apple trades near sixty times
// its book, and residual income put its fair value above six hundred
// dollars on a book value of five. That is not a valuation, it is an
// artefact, so the weight fades as price-to-book rises and is gone by the
// point book value is telling us nothing.
func bookTrust(price, bookPerShare float64) float64 {
	if bookPerShare <= 0 || price <= 0 {
		return 0
	}
	pb := price / bookPerShare
	switch {
	case pb <= 5:
		return 1
	case pb >= 15:
		return 0
	default:
		return (15 - pb) / 10
	}
}

// sustainablePayout is the share of earnings a company can pay out while
// still funding the growth it is expected to achieve. It stands in for a
// dividend where none is reported, which on this data is common.
func sustainablePayout(f market.Fundamentals, eps, g float64) float64 {
	if eps > 0 && f.DividendPerShare > 0 {
		return clamp(f.DividendPerShare/eps, 0, 0.9)
	}
	if roe := f.ReturnOnEquity; roe > 0 && g > 0 {
		return clamp(1-g/roe, 0.1, 0.9)
	}
	return 0.4
}

// Value runs the three models and blends what survives.
func Value(f market.Fundamentals, in Inputs, peHistory []float64) Result {
	r := CostOfEquity(in.RiskFree, in.Beta)
	g, source := Growth(f, in)
	out := Result{DiscountRate: r, Growth: g}

	if f.TrailingEPS > 0 {
		out.TrailingPE = in.Price / f.TrailingEPS
	}
	if len(f.ForwardEPS) > 0 && f.ForwardEPS[0] > 0 {
		out.ForwardPE = in.Price / f.ForwardEPS[0]
	}
	if source == "assumed" {
		out.Warnings = append(out.Warnings,
			"no earnings forecast or history; growth assumed at the long-run rate")
	}

	// Weights follow the evidence on finite-horizon accuracy: residual
	// income first, multiples second, an explicit earnings discount last.
	type candidate struct {
		method string
		weight float64
		value  float64
		ok     bool
		note   string
	}
	v1, ok1 := ResidualIncome(f, in, r, g)
	v2, ok2 := HistoricalMultiple(f, peHistory)
	v3, ok3 := EarningsDiscount(f, in, r, g)

	// Residual income leads on the evidence, but only where book value is
	// still describing the business.
	trust := bookTrust(in.Price, f.BookValuePerShare)
	out.BookTrust = trust
	if ok1 && trust < 1 {
		out.Warnings = append(out.Warnings,
			"book value is small against the share price, so the residual income estimate carries less weight")
	}
	cands := []candidate{
		{"residual income", 0.50 * trust, v1, ok1 && trust > 0, "book value plus earnings above the cost of capital"},
		{"historical multiple", 0.30, v2, ok2, "the multiple these shares have actually traded on"},
		{"earnings discount", 0.20, v3, ok3, "consensus earnings, discounted"},
	}

	var total, weighted float64
	for _, c := range cands {
		if !c.ok || c.value <= 0 {
			continue
		}
		out.Estimates = append(out.Estimates, Estimate{
			Method: c.method, Value: c.value, Weight: c.weight, Note: c.note,
		})
		total += c.weight
		weighted += c.weight * c.value
	}
	if total == 0 {
		out.Warnings = append(out.Warnings, "no valuation model could be applied to this company")
		return out
	}
	// Renormalise so a missing model shifts weight to the others rather
	// than dragging the estimate toward zero.
	for i := range out.Estimates {
		out.Estimates[i].Weight /= total
	}
	out.FairValue = weighted / total
	if in.Price > 0 {
		out.Upside = out.FairValue/in.Price - 1
	}
	out.Confidence, out.Spread = confidence(out.Estimates, out.FairValue)
	if out.Confidence == "low" {
		out.Warnings = append(out.Warnings,
			"the models disagree widely, so treat the fair value as a range rather than a number")
	}
	return out
}

// confidence reads how much the surviving models disagree. Averaging three
// estimates that span a factor of three produces a number with a decimal
// point and no information, and saying so is more useful than not.
func confidence(estimates []Estimate, blend float64) (string, float64) {
	if len(estimates) < 2 || blend <= 0 {
		return "low", 0
	}
	lo, hi := estimates[0].Value, estimates[0].Value
	for _, e := range estimates {
		lo = math.Min(lo, e.Value)
		hi = math.Max(hi, e.Value)
	}
	spread := (hi - lo) / blend
	switch {
	case spread <= 0.35 && len(estimates) >= 3:
		return "high", spread
	case spread <= 0.80:
		return "medium", spread
	default:
		return "low", spread
	}
}

func forwardOrTrailing(f market.Fundamentals) float64 {
	if len(f.ForwardEPS) > 0 && f.ForwardEPS[0] > 0 {
		return f.ForwardEPS[0]
	}
	return f.TrailingEPS
}

func clamp(v, lo, hi float64) float64 { return math.Min(hi, math.Max(lo, v)) }
