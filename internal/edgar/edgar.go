// Package edgar looks up recent SEC filings for a ticker. The SEC requires
// a User-Agent that identifies the caller with a contact address.
package edgar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/asrijanga/market-predictions/internal/cache"
	"github.com/asrijanga/market-predictions/internal/market"
)

// ErrNoContact is returned when no contact address is configured; the SEC
// rejects anonymous automated traffic.
var ErrNoContact = errors.New("edgar: contact email required (set -sec-contact or SEC_CONTACT_EMAIL)")

// Client fetches EDGAR data with an optional daily cache.
type Client struct {
	SECURL  string // https://www.sec.gov
	DataURL string // https://data.sec.gov
	HTTP    *http.Client
	Cache   *cache.Cache
	Contact string
}

// NewClient returns a client identified by contact.
func NewClient(httpClient *http.Client, c *cache.Cache, contact string) *Client {
	return &Client{
		SECURL:  "https://www.sec.gov",
		DataURL: "https://data.sec.gov",
		HTTP:    httpClient,
		Cache:   c,
		Contact: contact,
	}
}

// Forms worth surfacing to an analyst; everything else is noise.
var interestingForms = map[string]bool{"8-K": true, "10-Q": true, "10-K": true, "8-K/A": true, "10-Q/A": true, "10-K/A": true}

// RecentFilings returns filings for ticker filed on or after since, newest
// first, limited to periodic reports and 8-K current reports.
func (c *Client) RecentFilings(ctx context.Context, ticker string, since, day time.Time) ([]market.Filing, error) {
	if strings.TrimSpace(c.Contact) == "" {
		return nil, ErrNoContact
	}
	cik, err := c.lookupCIK(ctx, ticker, day)
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, fmt.Sprintf("%s/submissions/CIK%010d.json", c.DataURL, cik), fmt.Sprintf("edgar:sub:%d:%s", cik, day.Format(time.DateOnly)))
	if err != nil {
		return nil, err
	}
	var sub struct {
		Filings struct {
			Recent struct {
				Form        []string `json:"form"`
				FilingDate  []string `json:"filingDate"`
				Items       []string `json:"items"`
				Description []string `json:"primaryDocDescription"`
			} `json:"recent"`
		} `json:"filings"`
	}
	if err := json.Unmarshal(body, &sub); err != nil {
		return nil, fmt.Errorf("edgar: decode submissions: %w", err)
	}
	r := sub.Filings.Recent
	var out []market.Filing
	for i := range r.Form {
		if !interestingForms[r.Form[i]] {
			continue
		}
		t, err := time.Parse(time.DateOnly, r.FilingDate[i])
		if err != nil || t.Before(since) {
			continue
		}
		f := market.Filing{Filed: t, Form: r.Form[i]}
		if i < len(r.Items) {
			f.Items = r.Items[i]
		}
		if i < len(r.Description) {
			f.Description = r.Description[i]
		}
		out = append(out, f)
	}
	return out, nil
}

func (c *Client) lookupCIK(ctx context.Context, ticker string, day time.Time) (int, error) {
	body, err := c.get(ctx, c.SECURL+"/files/company_tickers.json", "edgar:tickers:"+day.Format(time.DateOnly))
	if err != nil {
		return 0, err
	}
	var table map[string]struct {
		CIK    int    `json:"cik_str"`
		Ticker string `json:"ticker"`
	}
	if err := json.Unmarshal(body, &table); err != nil {
		return 0, fmt.Errorf("edgar: decode tickers: %w", err)
	}
	want := strings.ToUpper(strings.ReplaceAll(ticker, "/", "-"))
	for _, row := range table {
		if strings.EqualFold(row.Ticker, want) {
			return row.CIK, nil
		}
	}
	return 0, fmt.Errorf("edgar: no CIK for %s", ticker)
}

func (c *Client) get(ctx context.Context, u, key string) ([]byte, error) {
	if body, ok := c.Cache.Get(key); ok {
		return body, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mktpredict/1.0 ("+c.Contact+")")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("edgar: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("edgar: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("edgar: HTTP %d for %s", resp.StatusCode, u)
	}
	_ = c.Cache.Put(key, body)
	return body, nil
}
