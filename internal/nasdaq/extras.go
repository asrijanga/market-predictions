package nasdaq

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/asrijanga/market-predictions/internal/market"
)

var earningsDateRE = regexp.MustCompile(`(\d{1,2}/\d{1,2}/\d{4})`)

// EarningsDate returns the next expected earnings date for symbol, or the
// zero time when Nasdaq has no estimate.
func (c *Client) EarningsDate(ctx context.Context, symbol string, day time.Time) (time.Time, error) {
	u := c.BaseURL + "/api/analyst/" + url.PathEscape(strings.ReplaceAll(symbol, "/", ".")) + "/earnings-date"
	body, err := c.get(ctx, u, "earnings:"+symbol+":"+day.Format(time.DateOnly))
	if err != nil {
		return time.Time{}, err
	}
	var resp struct {
		Data struct {
			ReportText   string `json:"reportText"`
			Announcement string `json:"announcement"`
		} `json:"data"`
		Status apiStatus `json:"status"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return time.Time{}, fmt.Errorf("nasdaq: decode earnings date: %w", err)
	}
	if err := resp.Status.err(); err != nil {
		return time.Time{}, err
	}
	if m := earningsDateRE.FindString(resp.Data.ReportText); m != "" {
		return time.Parse("1/2/2006", m)
	}
	// Fall back to the "Oct 29, 2026" announcement format.
	if i := strings.LastIndex(resp.Data.Announcement, ": "); i >= 0 {
		if t, err := time.Parse("Jan 2, 2006", strings.TrimSpace(resp.Data.Announcement[i+2:])); err == nil {
			return t, nil
		}
	}
	return time.Time{}, nil
}

// OptionChain returns every listed call and put expiring between from and
// to (inclusive), grouped by expiry then strike.
func (c *Client) OptionChain(ctx context.Context, symbol string, from, to time.Time) ([]market.OptionQuote, error) {
	q := url.Values{
		"assetclass": {"stocks"},
		"limit":      {"0"},
		"fromdate":   {from.Format(time.DateOnly)},
		"todate":     {to.Format(time.DateOnly)},
		"excode":     {"oprac"},
		"callput":    {"callput"},
		"money":      {"all"},
		"type":       {"all"},
	}
	u := c.BaseURL + "/api/quote/" + url.PathEscape(strings.ReplaceAll(symbol, "/", ".")) + "/option-chain?" + q.Encode()
	body, err := c.get(ctx, u, "options:"+symbol+":"+from.Format(time.DateOnly)+":"+to.Format(time.DateOnly)+":"+time.Now().UTC().Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			Table struct {
				Rows []struct {
					ExpiryGroup string `json:"expirygroup"`
					Strike      string `json:"strike"`
					CLast       string `json:"c_Last"`
					CBid        string `json:"c_Bid"`
					CAsk        string `json:"c_Ask"`
					CVolume     string `json:"c_Volume"`
					COI         string `json:"c_Openinterest"`
					PLast       string `json:"p_Last"`
					PBid        string `json:"p_Bid"`
					PAsk        string `json:"p_Ask"`
					PVolume     string `json:"p_Volume"`
					POI         string `json:"p_Openinterest"`
				} `json:"rows"`
			} `json:"table"`
		} `json:"data"`
		Status apiStatus `json:"status"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("nasdaq: decode option chain: %w", err)
	}
	if err := resp.Status.err(); err != nil {
		return nil, err
	}
	var out []market.OptionQuote
	var expiry time.Time
	for _, r := range resp.Data.Table.Rows {
		if r.ExpiryGroup != "" {
			t, err := time.Parse("January 2, 2006", r.ExpiryGroup)
			if err != nil {
				expiry = time.Time{}
				continue
			}
			expiry = t
			continue
		}
		strike := parseNumber(r.Strike)
		if expiry.IsZero() || strike <= 0 {
			continue
		}
		out = append(out, market.OptionQuote{
			Expiry: expiry, Strike: strike,
			Call: market.OptionSide{Last: parseNumber(r.CLast), Bid: parseNumber(r.CBid), Ask: parseNumber(r.CAsk), Volume: int64(parseNumber(r.CVolume)), OpenInterest: int64(parseNumber(r.COI))},
			Put:  market.OptionSide{Last: parseNumber(r.PLast), Bid: parseNumber(r.PBid), Ask: parseNumber(r.PAsk), Volume: int64(parseNumber(r.PVolume)), OpenInterest: int64(parseNumber(r.POI))},
		})
	}
	return out, nil
}
