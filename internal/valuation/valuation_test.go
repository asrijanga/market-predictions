package valuation

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/asrijanga/market-predictions/internal/market"
)

// A company earning exactly its cost of capital is worth its book value
// and no more: every dollar it makes is the rent on the capital it used.
// This is the one point where residual income has an exact answer, which
// makes it the right place to check the implementation.
func TestResidualIncomeAnchorsOnBookValueWhenReturnsAreOrdinary(t *testing.T) {
	in := Defaults(0.04)
	r := CostOfEquity(in.RiskFree, 1)
	const book float64 = 100
	f := market.Fundamentals{
		BookValuePerShare: book,
		ForwardEPS:        []float64{r * book}, // ROE exactly equals r
	}
	v, ok := ResidualIncome(f, in, r, 0.03)
	if !ok {
		t.Fatal("no estimate produced")
	}
	if math.Abs(v-book) > 0.5 {
		t.Errorf("fair value = %.2f, want book value %.2f: a company earning its cost of capital adds nothing", v, book)
	}
}

func TestResidualIncomeRewardsReturnsAboveTheCostOfCapital(t *testing.T) {
	in := Defaults(0.04)
	r := CostOfEquity(in.RiskFree, 1)
	const book float64 = 100
	ordinary := market.Fundamentals{BookValuePerShare: book, ForwardEPS: []float64{r * book}}
	excellent := market.Fundamentals{BookValuePerShare: book, ForwardEPS: []float64{2 * r * book}}

	a, _ := ResidualIncome(ordinary, in, r, 0.03)
	b, _ := ResidualIncome(excellent, in, r, 0.03)
	if b <= a {
		t.Errorf("doubling return on equity did not raise the value: %.2f vs %.2f", b, a)
	}
	if b <= book {
		t.Errorf("a company earning twice its cost of capital is worth more than book: %.2f", b)
	}
}

func TestCostOfEquityRisesWithBeta(t *testing.T) {
	low := CostOfEquity(0.04, 0.5)
	high := CostOfEquity(0.04, 2.0)
	if !(low < high) {
		t.Errorf("a riskier company must be discounted harder: %.4f vs %.4f", low, high)
	}
	// Whatever beta says, the rate stays inside a band that keeps the
	// discounting meaningful.
	if got := CostOfEquity(0.04, 50); got > 0.04+0.12+1e-9 {
		t.Errorf("an absurd beta escaped the clamp: %.4f", got)
	}
	if got := CostOfEquity(0.04, 0.001); got < 0.04+0.02-1e-9 {
		t.Errorf("a near-zero beta escaped the clamp: %.4f", got)
	}
}

func TestGrowthPrefersConsensusAndIsCapped(t *testing.T) {
	in := Defaults(0.04)
	f := market.Fundamentals{ForwardEPS: []float64{1, 1.1, 1.21}} // 10% a year
	g, source := Growth(f, in)
	if source != "consensus" {
		t.Errorf("source = %q, want consensus", source)
	}
	if math.Abs(g-0.10) > 0.005 {
		t.Errorf("growth = %.4f, want about 0.10", g)
	}
	// A forecast of a tenfold rise is a forecast, not a fact.
	wild := market.Fundamentals{ForwardEPS: []float64{1, 10}}
	if g, _ := Growth(wild, in); g > in.GrowthCap+1e-9 {
		t.Errorf("growth = %.4f, want it capped at %.4f", g, in.GrowthCap)
	}
}

func TestValueBlendsAndRenormalises(t *testing.T) {
	f := market.Fundamentals{
		BookValuePerShare: 50,
		TrailingEPS:       8,
		ForwardEPS:        []float64{9, 10, 11},
		DividendPerShare:  2,
		ReturnOnEquity:    0.18,
	}
	in := Defaults(0.04)
	in.Price = 100
	in.Beta = 1.1

	got := Value(f, in, nil)
	if got.FairValue <= 0 {
		t.Fatalf("no fair value: %+v", got.Warnings)
	}
	if len(got.Estimates) == 0 {
		t.Fatal("no estimates recorded")
	}
	var sum float64
	for _, e := range got.Estimates {
		sum += e.Weight
		if e.Value <= 0 {
			t.Errorf("%s produced %.2f", e.Method, e.Value)
		}
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Errorf("weights sum to %.6f, want 1", sum)
	}
	// The blend has to sit inside the range of what it blended.
	lo, hi := got.Estimates[0].Value, got.Estimates[0].Value
	for _, e := range got.Estimates {
		lo, hi = math.Min(lo, e.Value), math.Max(hi, e.Value)
	}
	if got.FairValue < lo-1e-9 || got.FairValue > hi+1e-9 {
		t.Errorf("blend %.2f outside the range [%.2f, %.2f]", got.FairValue, lo, hi)
	}
	if math.Abs(got.Upside-(got.FairValue/100-1)) > 1e-9 {
		t.Errorf("upside %.4f does not match fair value %.2f against price 100", got.Upside, got.FairValue)
	}
}

// The estimator has to recover a reversion speed it was given, or the
// half-life it reports means nothing.
func TestFitReversionRecoversAKnownSpeed(t *testing.T) {
	const (
		trueKappa = 2.0 // half-life of about four months
		trueSigma = 0.25
		perYear   = 252
	)
	rng := rand.New(rand.NewPCG(7, 11))
	dt := 1.0 / perYear
	x := 0.5
	ratios := make([]float64, 0, 1500)
	for i := 0; i < 1500; i++ {
		x += trueKappa*(0-x)*dt + trueSigma*math.Sqrt(dt)*rng.NormFloat64()
		ratios = append(ratios, math.Exp(x))
	}
	kappa, sigma, ok := FitReversion(ratios, perYear)
	if !ok {
		t.Fatalf("no fit; kappa=%.3f", kappa)
	}
	if math.Abs(kappa-trueKappa) > 0.8 {
		t.Errorf("kappa = %.3f, want about %.3f", kappa, trueKappa)
	}
	if math.Abs(sigma-trueSigma) > 0.1 {
		t.Errorf("sigma = %.3f, want about %.3f", sigma, trueSigma)
	}
}

// A ratio that wanders without returning must not be reported as reverting
// very slowly; there is no half-life to quote.
func TestFitReversionRefusesARandomWalk(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 5))
	x := 0.0
	ratios := make([]float64, 0, 1000)
	for i := 0; i < 1000; i++ {
		x += 0.02 * rng.NormFloat64()
		ratios = append(ratios, math.Exp(x))
	}
	if _, _, ok := FitReversion(ratios, 252); ok {
		t.Error("a random walk was reported as mean-reverting")
	}
}

func TestTimeToValueIsSoonerWhenReversionIsFaster(t *testing.T) {
	slow := TimeToValue(0.3, 0.5, 0.2, 5, 4000, 1)
	fast := TimeToValue(0.3, 4.0, 0.2, 5, 4000, 1)
	if !(fast.HalfLife < slow.HalfLife) {
		t.Errorf("half-life did not shorten: %.1f vs %.1f months", fast.HalfLife, slow.HalfLife)
	}
	if fast.MedianMonths == 0 || slow.MedianMonths == 0 {
		t.Fatalf("no median arrival: fast=%+v slow=%+v", fast, slow)
	}
	if !(fast.MedianMonths < slow.MedianMonths) {
		t.Errorf("faster reversion did not arrive sooner: %.1f vs %.1f months",
			fast.MedianMonths, slow.MedianMonths)
	}
	if fast.WithinYear < 0.5 {
		t.Errorf("only %.0f%% of fast-reverting paths arrived inside a year", 100*fast.WithinYear)
	}
}
