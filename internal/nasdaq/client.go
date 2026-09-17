// Package nasdaq fetches listings and daily price history from Nasdaq's
// public data API (api.nasdaq.com). No API key is required.
package nasdaq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asrijanga/market-predictions/internal/cache"
	"github.com/asrijanga/market-predictions/internal/market"
)

const (
	// DefaultBaseURL is the production Nasdaq data API endpoint.
	DefaultBaseURL = "https://api.nasdaq.com"

	// The API rejects requests without a browser-like User-Agent.
	userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)

// ErrSymbolNotFound is returned when the API reports an unknown symbol.
var ErrSymbolNotFound = errors.New("nasdaq: symbol not found")

// Client talks to the Nasdaq data API with retries and optional caching.
type Client struct {
	BaseURL    string
	HTTP       *http.Client
	Cache      *cache.Cache
	MaxRetries int
}

// NewClient returns a Client tuned for up to concurrency parallel requests.
// cache may be nil to disable caching.
func NewClient(concurrency int, c *cache.Cache) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = concurrency * 2
	transport.MaxIdleConnsPerHost = concurrency
	transport.ForceAttemptHTTP2 = true
	return &Client{
		BaseURL:    DefaultBaseURL,
		HTTP:       &http.Client{Transport: transport, Timeout: 45 * time.Second},
		Cache:      c,
		MaxRetries: 4,
	}
}

type apiStatus struct {
	RCode        int `json:"rCode"`
	BCodeMessage []struct {
		Code         int    `json:"code"`
		ErrorMessage string `json:"errorMessage"`
	} `json:"bCodeMessage"`
}

func (s apiStatus) err() error {
	if s.RCode == 0 || s.RCode == 200 {
		return nil
	}
	for _, m := range s.BCodeMessage {
		if strings.Contains(strings.ToLower(m.ErrorMessage), "not exist") {
			return ErrSymbolNotFound
		}
	}
	if len(s.BCodeMessage) > 0 {
		return fmt.Errorf("nasdaq: api error %d: %s", s.RCode, s.BCodeMessage[0].ErrorMessage)
	}
	return fmt.Errorf("nasdaq: api error %d", s.RCode)
}

// Listings returns every security in the Nasdaq stock screener (all US
// exchanges), unsorted. The result is cached for the calendar day.
func (c *Client) Listings(ctx context.Context, day time.Time) ([]market.Listing, error) {
	q := url.Values{
		"tableonly": {"true"},
		"limit":     {"0"},
		"download":  {"true"},
	}
	u := c.BaseURL + "/api/screener/stocks?" + q.Encode()
	body, err := c.get(ctx, u, "screener:"+day.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			Rows []struct {
				Symbol    string `json:"symbol"`
				Name      string `json:"name"`
				LastSale  string `json:"lastsale"`
				MarketCap string `json:"marketCap"`
				Volume    string `json:"volume"`
				Sector    string `json:"sector"`
				Industry  string `json:"industry"`
				Country   string `json:"country"`
			} `json:"rows"`
		} `json:"data"`
		Status apiStatus `json:"status"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("nasdaq: decode screener: %w", err)
	}
	if err := resp.Status.err(); err != nil {
		return nil, err
	}
	out := make([]market.Listing, 0, len(resp.Data.Rows))
	for _, r := range resp.Data.Rows {
		out = append(out, market.Listing{
			Symbol:    strings.TrimSpace(r.Symbol),
			Name:      strings.TrimSpace(r.Name),
			Sector:    r.Sector,
			Industry:  r.Industry,
			Country:   r.Country,
			LastSale:  parseNumber(r.LastSale),
			MarketCap: parseNumber(r.MarketCap),
			Volume:    int64(parseNumber(r.Volume)),
		})
	}
	return out, nil
}

// History returns daily bars for symbol between from and to (inclusive),
// oldest first. It tries the "stocks" asset class first and falls back to
// "etf" so index funds used as benchmarks resolve transparently.
func (c *Client) History(ctx context.Context, symbol string, from, to time.Time) ([]market.Bar, error) {
	bars, err := c.history(ctx, symbol, "stocks", from, to)
	if errors.Is(err, ErrSymbolNotFound) {
		bars, err = c.history(ctx, symbol, "etf", from, to)
	}
	return bars, err
}

func (c *Client) history(ctx context.Context, symbol, assetClass string, from, to time.Time) ([]market.Bar, error) {
	q := url.Values{
		"assetclass": {assetClass},
		"fromdate":   {from.Format(time.DateOnly)},
		"todate":     {to.Format(time.DateOnly)},
		"limit":      {"9999"},
	}
	apiSymbol := url.PathEscape(strings.ReplaceAll(symbol, "/", "."))
	u := c.BaseURL + "/api/quote/" + apiSymbol + "/historical?" + q.Encode()
	body, err := c.get(ctx, u, "history:"+assetClass+":"+symbol+":"+from.Format(time.DateOnly)+":"+to.Format(time.DateOnly))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", symbol, err)
	}
	var resp struct {
		Data struct {
			TradesTable struct {
				Rows []struct {
					Date   string `json:"date"`
					Close  string `json:"close"`
					Volume string `json:"volume"`
					Open   string `json:"open"`
					High   string `json:"high"`
					Low    string `json:"low"`
				} `json:"rows"`
			} `json:"tradesTable"`
		} `json:"data"`
		Status apiStatus `json:"status"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("nasdaq: decode history for %s: %w", symbol, err)
	}
	if err := resp.Status.err(); err != nil {
		return nil, fmt.Errorf("%s: %w", symbol, err)
	}
	rows := resp.Data.TradesTable.Rows
	bars := make([]market.Bar, 0, len(rows))
	for _, r := range rows {
		d, err := time.Parse("01/02/2006", r.Date)
		if err != nil {
			continue
		}
		closePx := parseNumber(r.Close)
		if closePx <= 0 {
			continue
		}
		bars = append(bars, market.Bar{
			Date:   d,
			Open:   parseNumber(r.Open),
			High:   parseNumber(r.High),
			Low:    parseNumber(r.Low),
			Close:  closePx,
			Volume: int64(parseNumber(r.Volume)),
		})
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].Date.Before(bars[j].Date) })
	return bars, nil
}

// get performs a cached GET with exponential backoff on transient failures.
// Any well-formed API answer is cached, including "symbol not found", since
// cache keys carry the request date and so expire with the trading day.
func (c *Client) get(ctx context.Context, rawURL, cacheKey string) ([]byte, error) {
	if body, ok := c.Cache.Get(cacheKey); ok {
		return body, nil
	}
	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<uint(attempt-1))*time.Second + rand.N(500*time.Millisecond)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		body, retry, err := c.doOnce(ctx, rawURL)
		if err == nil {
			if json.Valid(body) {
				// A cache write failure must not fail the request.
				_ = c.Cache.Put(cacheKey, body)
			}
			return body, nil
		}
		lastErr = err
		if !retry || ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}

// doOnce executes a single request. The bool reports whether the error is
// transient and the request should be retried.
func (c *Client) doOnce(ctx context.Context, rawURL string) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		var nerr net.Error
		transient := errors.As(err, &nerr) || errors.Is(err, io.ErrUnexpectedEOF)
		return nil, transient, fmt.Errorf("nasdaq: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, true, fmt.Errorf("nasdaq: read body: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return body, false, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, true, fmt.Errorf("nasdaq: HTTP %d for %s", resp.StatusCode, rawURL)
	default:
		return nil, false, fmt.Errorf("nasdaq: HTTP %d for %s", resp.StatusCode, rawURL)
	}
}

// parseNumber converts strings such as "$1,234.56", "31,748,180" or "NA"
// into a float. Unparseable input yields 0.
func parseNumber(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "NA" || s == "N/A" {
		return 0
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch ch := s[i]; {
		case ch >= '0' && ch <= '9', ch == '.', ch == '-':
			b.WriteByte(ch)
		}
	}
	v, err := strconv.ParseFloat(b.String(), 64)
	if err != nil {
		return 0
	}
	return v
}
