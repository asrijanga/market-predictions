// Package universe narrows an exchange screener down to liquid, optionable
// common stocks worth analysing.
package universe

import (
	"sort"
	"strings"

	"github.com/asrijanga/market-predictions/internal/market"
)

// Options control which listings survive selection.
type Options struct {
	// Top keeps only the N largest survivors by market capitalisation.
	Top int
	// MinPrice drops penny stocks whose options are illiquid.
	MinPrice float64
	// MinDollarVolume drops names with thin daily turnover (price × volume).
	MinDollarVolume float64
}

// Name fragments that indicate a security is not a plain common stock.
var excludedNameFragments = []string{
	"warrant", "right", " unit", "preferred", "depositary shares each",
	"notes due", "acquisition corp", "acquisition co", "debenture",
	"% senior", "trust preferred", "subordinated",
}

// Select filters and ranks listings, returning at most opts.Top entries
// ordered by descending market cap. Multiple share classes of one issuer
// collapse to the most liquid class.
func Select(listings []market.Listing, opts Options) []market.Listing {
	byIssuer := make(map[string]market.Listing, len(listings))
	for _, l := range listings {
		if !eligible(l, opts) {
			continue
		}
		key := issuerKey(l.Name)
		if prev, ok := byIssuer[key]; ok && dollarVolume(prev) >= dollarVolume(l) {
			continue
		}
		byIssuer[key] = l
	}
	out := make([]market.Listing, 0, len(byIssuer))
	for _, l := range byIssuer {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MarketCap != out[j].MarketCap {
			return out[i].MarketCap > out[j].MarketCap
		}
		return out[i].Symbol < out[j].Symbol
	})
	if opts.Top > 0 && len(out) > opts.Top {
		out = out[:opts.Top]
	}
	return out
}

func eligible(l market.Listing, opts Options) bool {
	if l.Symbol == "" || strings.ContainsAny(l.Symbol, "^$") {
		return false
	}
	if l.MarketCap <= 0 || l.LastSale < opts.MinPrice {
		return false
	}
	if dollarVolume(l) < opts.MinDollarVolume {
		return false
	}
	name := strings.ToLower(l.Name)
	for _, frag := range excludedNameFragments {
		if strings.Contains(name, frag) {
			return false
		}
	}
	return true
}

func dollarVolume(l market.Listing) float64 {
	return l.LastSale * float64(l.Volume)
}

// issuerKey normalises a security name so that "Alphabet Inc. Class A
// Common Stock" and "Alphabet Inc. Class C Capital Stock" share a key.
func issuerKey(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	for _, cut := range []string{" class ", " common stock", " ordinary shares", " american depositary", " capital stock", " depositary"} {
		if i := strings.Index(s, cut); i >= 0 {
			s = s[:i]
		}
	}
	return strings.TrimRight(s, " .,-")
}
