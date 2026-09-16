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
