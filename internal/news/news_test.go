package news

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const feed = `<?xml version="1.0"?><rss version="2.0"><channel><title>x</title>
<item><title>Apple shares rally - CNBC</title><link>https://a</link><pubDate>Wed, 02 Sep 2026 07:00:00 GMT</pubDate><source url="https://cnbc.com">CNBC</source></item>
<item><title>Old news - Reuters</title><link>https://b</link><pubDate>Wed, 01 Jan 2025 07:00:00 GMT</pubDate><source url="https://reuters.com">Reuters</source></item>
<item><title>Newer &amp; better - Barron's</title><link>https://c</link><pubDate>Thu, 10 Sep 2026 07:00:00 GMT</pubDate><source url="https://barrons.com">Barron's</source></item>
</channel></rss>`

func TestHeadlines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "AAPL stock" {
			t.Errorf("query = %q", r.URL.Query().Get("q"))
		}
		w.Write([]byte(feed))
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), nil)
	c.BaseURL = srv.URL
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	got, err := c.Headlines(context.Background(), "AAPL stock", since, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d headlines, want 2 (old one filtered)", len(got))
	}
	if got[0].Title != "Newer & better" || got[0].Source != "Barron's" {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Title != "Apple shares rally" {
		t.Errorf("second = %+v", got[1])
	}
}
