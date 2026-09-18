// Package market defines the core data types shared across the application.
package market

import "time"

// Bar is one trading day of OHLCV data.
type Bar struct {
	Date   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume int64
}

// Listing is a tradable security as described by an exchange screener.
type Listing struct {
	Symbol    string
	Name      string
	Sector    string
	Industry  string
	Country   string
	LastSale  float64
	MarketCap float64
	Volume    int64
}

// OptionSide is one leg (call or put) of an option quote.
type OptionSide struct {
	Last         float64 `json:"last"`
	Bid          float64 `json:"bid"`
	Ask          float64 `json:"ask"`
	Volume       int64   `json:"volume"`
	OpenInterest int64   `json:"open_interest"`
}

// Mid returns the bid/ask midpoint, falling back to the last trade when
// either side of the market is missing.
func (s OptionSide) Mid() float64 {
	if s.Bid > 0 && s.Ask > 0 {
		return (s.Bid + s.Ask) / 2
	}
	return s.Last
}

// OptionQuote is one strike on one expiry with both call and put legs.
type OptionQuote struct {
	Expiry time.Time  `json:"expiry"`
	Strike float64    `json:"strike"`
	Call   OptionSide `json:"call"`
	Put    OptionSide `json:"put"`
}

// Headline is one news item about a symbol.
type Headline struct {
	Published time.Time `json:"published"`
	Title     string    `json:"title"`
	Source    string    `json:"source"`
	Link      string    `json:"link"`
}

// Filing is one SEC EDGAR submission.
type Filing struct {
	Filed       time.Time `json:"filed"`
	Form        string    `json:"form"`
	Description string    `json:"description,omitempty"`
	Items       string    `json:"items,omitempty"`
}

// Fundamentals is what a company reports about itself, reduced to the
// figures a valuation needs. Everything is per share unless the name says
// otherwise, and a zero means the filing did not carry the line.
type Fundamentals struct {
	Symbol   string `json:"symbol"`
	Sector   string `json:"sector,omitempty"`
	Industry string `json:"industry,omitempty"`

	// Shares is derived, not reported: market capitalisation over price.
	Shares    float64 `json:"shares"`
	MarketCap float64 `json:"market_cap"`

	// TrailingEPS sums the last four reported quarters. ForwardEPS is the
	// analysts' consensus for each coming fiscal year, nearest first.
	TrailingEPS float64   `json:"trailing_eps"`
	ForwardEPS  []float64 `json:"forward_eps,omitempty"`
	// EPSEstimates counts the analysts behind the first forward year, and
	// EPSDispersion is the high-low spread over the consensus. Both say how
	// much weight a forecast deserves.
	EPSEstimates  int     `json:"eps_estimates,omitempty"`
	EPSDispersion float64 `json:"eps_dispersion,omitempty"`

	// QuarterlyEPS holds the reported quarters, oldest first, which is what
	// a trailing multiple is built from through time.
	QuarterlyEPS []QuarterEPS `json:"quarterly_eps,omitempty"`

	BookValuePerShare float64 `json:"book_value_per_share"`
	DividendPerShare  float64 `json:"dividend_per_share"`

	// Annual figures, most recent first.
	Revenue         []float64 `json:"revenue,omitempty"`
	NetIncome       []float64 `json:"net_income,omitempty"`
	Equity          []float64 `json:"equity,omitempty"`
	FreeCashFlow    []float64 `json:"free_cash_flow,omitempty"`
	ReturnOnEquity  float64   `json:"return_on_equity"`
	OperatingMargin float64   `json:"operating_margin"`
	Debt            float64   `json:"debt"`
	Cash            float64   `json:"cash"`

	FiscalEnds []string `json:"fiscal_ends,omitempty"`
}

// QuarterEPS is one reported quarter.
type QuarterEPS struct {
	Period   string    `json:"period"`
	End      time.Time `json:"end"`
	Reported float64   `json:"reported"`
}

// HasEarnings reports whether there is enough to value the company on what
// it earns rather than on its price alone.
func (f Fundamentals) HasEarnings() bool {
	return f.TrailingEPS > 0 || len(f.ForwardEPS) > 0
}
