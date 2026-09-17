// Package news fetches recent headlines for a symbol from Google News RSS.
package news

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/asrijanga/market-predictions/internal/cache"
	"github.com/asrijanga/market-predictions/internal/market"
)

// Client fetches headlines with an optional daily cache.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	Cache   *cache.Cache
}

// NewClient returns a client for news.google.com.
func NewClient(httpClient *http.Client, c *cache.Cache) *Client {
	return &Client{BaseURL: "https://news.google.com", HTTP: httpClient, Cache: c}
}

type rss struct {
	Channel struct {
		Items []struct {
			Title   string `xml:"title"`
			Link    string `xml:"link"`
			PubDate string `xml:"pubDate"`
			Source  string `xml:"source"`
		} `xml:"item"`
	} `xml:"channel"`
}

// Headlines returns headlines matching the query published on or after
// since, newest first. Google returns at most ~100 items per query.
func (c *Client) Headlines(ctx context.Context, query string, since, day time.Time) ([]market.Headline, error) {
	q := url.Values{"q": {query}, "hl": {"en-US"}, "gl": {"US"}, "ceid": {"US:en"}}
	u := c.BaseURL + "/rss/search?" + q.Encode()
	key := "news:" + query + ":" + day.Format(time.DateOnly)
	body, ok := c.Cache.Get(key)
	if !ok {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) mktpredict/1.0")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("news: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("news: HTTP %d", resp.StatusCode)
		}
		if body, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20)); err != nil {
			return nil, fmt.Errorf("news: read: %w", err)
		}
		_ = c.Cache.Put(key, body)
	}
	var feed rss
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("news: decode rss: %w", err)
	}
	out := make([]market.Headline, 0, len(feed.Channel.Items))
	for _, it := range feed.Channel.Items {
		t, err := time.Parse(time.RFC1123, it.PubDate)
		if err != nil {
			if t, err = time.Parse(time.RFC1123Z, it.PubDate); err != nil {
				continue
			}
		}
		if t.Before(since) {
			continue
		}
		title := html.UnescapeString(strings.TrimSpace(it.Title))
		// Google appends " - Source" to titles; strip it when it matches.
		if suffix := " - " + it.Source; it.Source != "" && strings.HasSuffix(title, suffix) {
			title = strings.TrimSuffix(title, suffix)
		}
		out = append(out, market.Headline{Published: t, Title: title, Source: it.Source, Link: it.Link})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Published.After(out[j].Published) })
	return out, nil
}
