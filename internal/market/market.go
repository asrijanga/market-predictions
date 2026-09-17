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
