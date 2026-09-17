package pack

import (
	"strings"
	"testing"
	"time"

	"github.com/asrijanga/market-predictions/internal/market"
	"github.com/asrijanga/market-predictions/internal/options"
	"github.com/asrijanga/market-predictions/internal/quant"
)

func bars(n int) []market.Bar {
	out := make([]market.Bar, n)
	p := 100.0
	start := time.Date(2025, 9, 17, 0, 0, 0, 0, time.UTC)
	d := start
	for i := range out {
		for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			d = d.AddDate(0, 0, 1)
		}
		p *= 1.001
		out[i] = market.Bar{Date: d, Open: p, High: p * 1.01, Low: p * 0.99, Close: p, Volume: 1e6}
		d = d.AddDate(0, 0, 1)
	}
	return out
}

func TestWeeklyCompressesByISOWeek(t *testing.T) {
	w := weekly(bars(252))
	if len(w) < 50 || len(w) > 53 {
		t.Fatalf("got %d weekly bars", len(w))
	}
	if w[0].Volume != 3e6 && w[0].Volume != 5e6 { // first week is partial (Wed start) or full
		t.Errorf("first week volume = %d", w[0].Volume)
	}
	if w[10].Volume != 5e6 {
		t.Errorf("full week volume = %d, want 5e6", w[10].Volume)
	}
}

func TestCalendarIncludesEarningsAndExpiries(t *testing.T) {
	from := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	ev := calendar(from, from.AddDate(0, 3, 0), time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC))
	var names []string
	for _, e := range ev {
		names = append(names, e.Date.Format("01-02")+" "+e.Name)
	}
	joined := strings.Join(names, "; ")
	for _, want := range []string{"10-16 Monthly", "11-20 Monthly", "10-28 FOMC", "10-29 Earnings"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	for i := 1; i < len(ev); i++ {
		if ev[i].Date.Before(ev[i-1].Date) {
			t.Fatal("events not sorted")
		}
	}
}

func TestRenderMentionsKeySections(t *testing.T) {
	b := bars(252)
	bench := quant.SeriesStats(quant.Closes(b), 126)
	trend, err := quant.Analyze("TEST", b, bench, 126, 63)
	if err != nil {
		t.Fatal(err)
	}
	now := b[len(b)-1].Date
	exp := quant.ThirdFriday(2026, time.November)
	p := &Pack{
		Symbol: "TEST", AsOf: now, Price: b[len(b)-1].Close, Benchmark: "SPY", HorizonDays: 63,
		TargetDate: now.AddDate(0, 0, 88), Trend: trend, Year: yearStats(b, b), Bars: b, Weekly: weekly(b),
		EarningsDate: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC), DaysToEarnings: 30,
		Headlines: []market.Headline{{Published: now, Title: "Test rallies", Source: "Wire"}},
		Filings:   []market.Filing{{Filed: now, Form: "8-K", Items: "2.02"}},
		Expiries: options.Summarize([]market.OptionQuote{
			{Expiry: exp, Strike: 130, Call: market.OptionSide{Bid: 5, Ask: 5.2}, Put: market.OptionSide{Bid: 4, Ask: 4.2}},
		}, b[len(b)-1].Close, now, 0.04),
		Warnings: []string{"news: partial"},
	}
	md := Render(p)
	for _, want := range []string{"# TEST data pack", "Next earnings: 2026-10-29", "Data warnings: news: partial",
		"Test rallies", "8-K items 2.02", "## Weekly closes", "### 2026-11-20"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q", want)
		}
	}
}
