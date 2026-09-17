package model

import (
	"encoding/json"
	"fmt"
	"math"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/asrijanga/market-predictions/internal/options"
	"github.com/asrijanga/market-predictions/internal/quant"
)

// Contract is one listed call the search may buy.
type Contract struct {
	Expiry       time.Time `json:"expiry"`
	ExpiryIdx    int       `json:"expiry_idx"` // trading days from today
	Strike       float64   `json:"strike"`
	MarketMid    float64   `json:"market_mid"`
	SpreadPct    float64   `json:"spread_pct"`
	OpenInterest int64     `json:"open_interest"`
	MarketIV     float64   `json:"market_iv"`
	ModelIV      float64   `json:"model_iv"`    // forecast volatility over the option's life
	ModelPrice   float64   `json:"model_price"` // Black-Scholes at the forecast volatility
	VolEdge      float64   `json:"vol_edge"`    // model IV minus market IV, in vol points
	Delta        float64   `json:"delta"`
	Breakeven    float64   `json:"breakeven"`
}

// Window is a range of trading days in which an entry may occur.
type Window struct {
	StartIdx int       `json:"start_idx"`
	EndIdx   int       `json:"end_idx"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Label    string    `json:"label,omitempty"`
}

// Trigger kinds.
const (
	TriggerImmediate = "immediate"
	TriggerDip       = "dip"
	TriggerBreakout  = "breakout"
)

// Trigger is the condition that has to hold for an entry to happen. Level
// is a ratio to today's spot; it is unused for immediate entries.
type Trigger struct {
	Kind  string  `json:"kind"`
	Level float64 `json:"level"`
}

// RankingStake is the fraction of capital the ranking criterion assumes is
// put on one trade. Expected log growth at this stake is what plans are
// sorted by: unlike expected return it is finite and concave, and therefore
// unimpressed by a lottery ticket that pays 20x on 4% of paths and nothing
// on the rest.
const RankingStake = 0.10

// Stats summarises one strategy over the simulated paths.
type Stats struct {
	PTrade        float64 `json:"p_trade"`     // probability the trigger fires inside the window
	MeanReturn    float64 `json:"mean_return"` // expected return per dollar, given a fill
	MedianReturn  float64 `json:"median_return"`
	PProfit       float64 `json:"p_profit"` // probability the option is worth more than it cost
	PDoubled      float64 `json:"p_doubled"`
	ExpectedValue float64 `json:"expected_value"` // PTrade × MeanReturn: value of committing to the plan
	GrowthRate    float64 `json:"growth_rate"`    // E[log(1 + stake × return)] at RankingStake
	KellyFraction float64 `json:"kelly_fraction"` // growth-maximising stake, 0 if none beats cash
	MeanCost      float64 `json:"mean_cost"`
	MeanEntry     float64 `json:"mean_entry_spot"`
	MeanPayoff    float64 `json:"mean_payoff"`
}

// Strategy is a contract bought under a trigger inside a window, with the
// simulated distribution of outcomes.
type Strategy struct {
	Window   Window   `json:"window"`
	Trigger  Trigger  `json:"trigger"`
	Contract Contract `json:"contract"`
	Stats    Stats    `json:"stats"`
	// DriftCurve is the expected return under each drift on DriftGrid, and
	// BreakEvenDrift the annual drift at which the plan returns zero. They
	// separate what the option structure contributes from what the trend
	// assumption contributes.
	DriftCurve     []float64 `json:"drift_curve,omitempty"`
	BreakEvenDrift BreakEven `json:"break_even_drift"`
}

// BreakEven is a drift level that may lie outside the simulated grid, in
// which case it is infinite and has no meaningful JSON value.
type BreakEven float64

// MarshalJSON renders an out-of-range break-even point as null rather than
// failing the encode, which is what a bare infinity would do.
func (b BreakEven) MarshalJSON() ([]byte, error) {
	f := float64(b)
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return []byte("null"), nil
	}
	return json.Marshal(f)
}

// minRunway is the least number of trading days that must remain between an
// entry and expiry. A month is deliberate: with less, a trigger that only
// fires after an adverse move buys a nearly worthless far-out-of-the-money
// call, and the search starts optimising for lottery tickets.
const minRunway = 21

// Evaluate prices one strategy across every simulated path.
func Evaluate(sim *Sim, spot0 float64, c Contract, w Window, tr Trigger, ivByEntry []float64, rate float64) Stats {
	st, _ := EvaluateDetailed(sim, spot0, c, w, tr, ivByEntry, rate)
	return st
}

// EvaluateDetailed is Evaluate plus the per-path returns, which the callers
// that need a Kelly stake use and the ranking search discards.
func EvaluateDetailed(sim *Sim, spot0 float64, c Contract, w Window, tr Trigger, ivByEntry []float64, rate float64) (Stats, []float64) {
	var st Stats
	if sim.Paths == 0 {
		return st, nil
	}
	slip := 1 + 0.5*math.Max(0, c.SpreadPct)
	returns := make([]float64, 0, sim.Paths)
	var traded, profit, doubled int
	var sumCost, sumEntry, sumPayoff float64

	for p := 0; p < sim.Paths; p++ {
		entry := -1
		for d := w.StartIdx; d <= w.EndIdx; d++ {
			if d > c.ExpiryIdx-minRunway {
				break
			}
			ratio := sim.At(p, d)
			switch tr.Kind {
			case TriggerImmediate:
				entry = d
			case TriggerDip:
				if ratio <= tr.Level {
					entry = d
				}
			case TriggerBreakout:
				if ratio >= tr.Level {
					entry = d
				}
			}
			if entry >= 0 {
				break
			}
		}
		if entry < 0 {
			continue
		}
		entrySpot := spot0 * sim.At(p, entry)
		years := float64(c.ExpiryIdx-entry) / quant.TradingDaysPerYear
		cost := options.CallPrice(entrySpot, c.Strike, years, rate, ivByEntry[entry]) * slip
		if cost <= 0.01 {
			continue
		}
		payoff := math.Max(spot0*sim.At(p, c.ExpiryIdx)-c.Strike, 0)
		traded++
		sumCost += cost
		sumEntry += entrySpot
		sumPayoff += payoff
		ret := payoff/cost - 1
		returns = append(returns, ret)
		if payoff > cost {
			profit++
		}
		if payoff > 2*cost {
			doubled++
		}
	}
	if traded == 0 {
		return st, nil
	}
	n := float64(traded)
	st.PTrade = n / float64(sim.Paths)
	st.MeanReturn = quant.Mean(returns)
	sort.Float64s(returns)
	st.MedianReturn = returns[len(returns)/2]
	st.PProfit = float64(profit) / n
	st.PDoubled = float64(doubled) / n
	st.ExpectedValue = st.PTrade * st.MeanReturn
	st.MeanCost = sumCost / n
	st.MeanEntry = sumEntry / n
	st.MeanPayoff = sumPayoff / n
	st.GrowthRate = growthRate(returns, RankingStake)
	return st, returns
}

// growthRate is the expected logarithmic growth of capital when a fraction
// stake is committed to the trade and the rest is held in cash.
func growthRate(returns []float64, stake float64) float64 {
	if len(returns) == 0 || stake <= 0 || stake >= 1 {
		return 0
	}
	var sum float64
	for _, r := range returns {
		sum += math.Log(1 + stake*math.Max(r, -1))
	}
	return sum / float64(len(returns))
}

// KellyFraction returns the stake that maximises expected log growth, found
// by a grid search. Zero means no stake beats holding cash.
func KellyFraction(returns []float64) float64 {
	best, bestG := 0.0, 0.0
	for f := 0.01; f <= 0.99; f += 0.01 {
		if g := growthRate(returns, f); g > bestG {
			best, bestG = f, g
		}
	}
	return best
}

// ivSchedule precomputes the implied volatility this contract would be
// quoted at for an entry on each day, which is where the post-earnings
// volatility crush enters the arithmetic.
func ivSchedule(m IVModel, c Contract, spot0 float64, horizon int) []float64 {
	out := make([]float64, horizon+1)
	for d := 0; d <= horizon; d++ {
		left := c.ExpiryIdx - d
		if left <= 0 {
			out[d] = m.BaseVol
			continue
		}
		eventAhead := m.EarningsIdx > d && m.EarningsIdx <= c.ExpiryIdx
		out[d] = m.Quote(c.Strike, spot0, left, eventAhead)
	}
	return out
}

// BuildWindows lays a weekly grid of one- and two-week entry windows over
// the search horizon, plus windows anchored to the earnings date, which is
// the one day whose position matters more than the calendar.
func BuildWindows(now time.Time, horizon, earningsIdx int) []Window {
	seen := map[[2]int]int{} // window bounds to index in out
	var out []Window
	add := func(start, end int, label string) {
		if start < 0 {
			start = 0
		}
		if end > horizon {
			end = horizon
		}
		if start > end {
			return
		}
		if i, ok := seen[[2]int{start, end}]; ok {
			// Keep the more informative label when the weekly grid already
			// produced the same range.
			if out[i].Label == "" {
				out[i].Label = label
			}
			return
		}
		seen[[2]int{start, end}] = len(out)
		out = append(out, Window{
			StartIdx: start, EndIdx: end,
			Start: TradingDayDate(now, start), End: TradingDayDate(now, end),
			Label: label,
		})
	}
	add(0, 0, "buy today")
	for start := 0; start <= horizon; start += 5 {
		add(start, start+4, "")
		add(start, start+9, "")
	}
	if earningsIdx > 0 {
		add(max(0, earningsIdx-10), earningsIdx-1, "before earnings")
		add(earningsIdx+1, earningsIdx+5, "after earnings")
		add(earningsIdx+1, earningsIdx+10, "after earnings")
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartIdx != out[j].StartIdx {
			return out[i].StartIdx < out[j].StartIdx
		}
		return out[i].EndIdx < out[j].EndIdx
	})
	return out
}

// DefaultTriggers are the entry conditions the search considers: take the
// market as it is, wait for a pullback, or wait for a breakout.
func DefaultTriggers() []Trigger {
	return []Trigger{
		{TriggerImmediate, 0},
		{TriggerDip, 0.97},
		{TriggerDip, 0.95},
		{TriggerDip, 0.92},
		{TriggerBreakout, 1.02},
		{TriggerBreakout, 1.04},
	}
}

// Search evaluates every contract × window × trigger combination and returns
// them ranked by expected value. Work is spread across CPUs.
func Search(sim *Sim, spot0 float64, m IVModel, contracts []Contract, windows []Window, triggers []Trigger, rate float64) []Strategy {
	schedules := make([][]float64, len(contracts))
	for i, c := range contracts {
		schedules[i] = ivSchedule(m, c, spot0, sim.Horizon)
	}
	type job struct{ ci, wi, ti int }
	var jobs []job
	for ci := range contracts {
		for wi := range windows {
			if windows[wi].StartIdx > contracts[ci].ExpiryIdx-minRunway {
				continue
			}
			for ti := range triggers {
				jobs = append(jobs, job{ci, wi, ti})
			}
		}
	}
	out := make([]Strategy, len(jobs))
	workers := min(runtime.NumCPU(), max(1, len(jobs)))
	var wg sync.WaitGroup
	next := make(chan int, len(jobs))
	for i := range jobs {
		next <- i
	}
	close(next)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				j := jobs[i]
				out[i] = Strategy{
					Window:   windows[j.wi],
					Trigger:  triggers[j.ti],
					Contract: contracts[j.ci],
					Stats:    Evaluate(sim, spot0, contracts[j.ci], windows[j.wi], triggers[j.ti], schedules[j.ci], rate),
				}
			}
		}()
	}
	wg.Wait()
	// Rank by expected log growth. Ranking by expected value would reward
	// triggers that almost never fire, because a plan that rarely trades has
	// an expected value near zero and so looks good whenever entering at all
	// is unattractive; ranking by expected return would reward the most
	// convex lottery ticket on the board.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Stats.GrowthRate != out[j].Stats.GrowthRate {
			return out[i].Stats.GrowthRate > out[j].Stats.GrowthRate
		}
		return out[i].Stats.PProfit > out[j].Stats.PProfit
	})
	return out
}

// TopDistinct keeps the best strategy per trigger, so the ranking answers
// the question that was asked - when to enter - instead of filling up with
// one trigger repeated at a dozen neighbouring strikes and date ranges.
func TopDistinct(all []Strategy, n int, minPTrade float64) []Strategy {
	seen := map[string]bool{}
	var out []Strategy
	for _, s := range all {
		if s.Stats.PTrade < minPTrade || s.Stats.PTrade == 0 {
			continue
		}
		key := fmt.Sprintf("%s|%.3f", s.Trigger.Kind, s.Trigger.Level)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
		if len(out) >= n {
			break
		}
	}
	return out
}

// TradingDayDate returns the calendar date n trading days after from,
// counting weekdays only (exchange holidays are not modelled).
func TradingDayDate(from time.Time, n int) time.Time {
	d := from
	for i := 0; i < n; i++ {
		d = d.AddDate(0, 0, 1)
		for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			d = d.AddDate(0, 0, 1)
		}
	}
	return d
}
