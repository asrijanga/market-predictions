package universe

import (
	"testing"

	"github.com/asrijanga/market-predictions/internal/market"
)

func TestSelectDedupesShareClassesAndFilters(t *testing.T) {
	in := []market.Listing{
		{Symbol: "GOOGL", Name: "Alphabet Inc. Class A Common Stock", LastSale: 200, MarketCap: 3e12, Volume: 30e6},
		{Symbol: "GOOG", Name: "Alphabet Inc. Class C Capital Stock", LastSale: 200, MarketCap: 3e12, Volume: 20e6},
		{Symbol: "BRK/A", Name: "Berkshire Hathaway Inc. Class A Common Stock", LastSale: 700000, MarketCap: 1e12, Volume: 1000},
		{Symbol: "BRK/B", Name: "Berkshire Hathaway Inc. Class B Common Stock", LastSale: 500, MarketCap: 1e12, Volume: 4e6},
		{Symbol: "AAC", Name: "Ares Acquisition Corporation III Class A Ordinary Shares", LastSale: 10, MarketCap: 7e8, Volume: 2e6},
		{Symbol: "PENNY", Name: "Penny Co Common Stock", LastSale: 1, MarketCap: 5e9, Volume: 100e6},
		{Symbol: "THIN", Name: "Thin Co Common Stock", LastSale: 100, MarketCap: 5e9, Volume: 1000},
		{Symbol: "OK", Name: "Fine Co Common Stock", LastSale: 50, MarketCap: 5e9, Volume: 2e6},
	}
	got := Select(in, Options{Top: 10, MinPrice: 5, MinDollarVolume: 2e7})
	want := []string{"GOOGL", "BRK/B", "OK"}
	if len(got) != len(want) {
		t.Fatalf("got %d listings %v, want %v", len(got), symbols(got), want)
	}
	for i := range want {
		if got[i].Symbol != want[i] {
			t.Errorf("rank %d = %s, want %s", i, got[i].Symbol, want[i])
		}
	}
}

func TestSelectTop(t *testing.T) {
	in := []market.Listing{
		{Symbol: "A", Name: "A Co", LastSale: 50, MarketCap: 1e9, Volume: 1e6},
		{Symbol: "B", Name: "B Co", LastSale: 50, MarketCap: 3e9, Volume: 1e6},
		{Symbol: "C", Name: "C Co", LastSale: 50, MarketCap: 2e9, Volume: 1e6},
	}
	got := Select(in, Options{Top: 2, MinPrice: 5, MinDollarVolume: 1e6})
	if len(got) != 2 || got[0].Symbol != "B" || got[1].Symbol != "C" {
		t.Fatalf("got %v", symbols(got))
	}
}

func symbols(ls []market.Listing) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Symbol
	}
	return out
}
