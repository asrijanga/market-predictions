package nasdaq

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/asrijanga/market-predictions/internal/market"
)

// Fundamentals gathers what the company reports about itself.
//
// Four endpoints answer different halves of the question and none of them
// is complete on its own: the statements carry the balance sheet and cash
// flows but no per-share figures, the EPS endpoint carries reported
// quarters, the forecast endpoint carries the consensus, and the summary
// carries the market capitalisation that turns totals into per-share
// numbers. A failure in any one of them degrades the valuation rather than
// failing it, so each is folded in on a best-effort basis.
func (c *Client) Fundamentals(ctx context.Context, symbol string, price float64, day time.Time) (market.Fundamentals, error) {
	f := market.Fundamentals{Symbol: symbol}
	stamp := day.Format(time.DateOnly)

	if err := c.summaryInto(ctx, symbol, price, stamp, &f); err != nil {
		return f, err
	}
	// Without statements there is no book value and no residual income, but
	// an earnings multiple still works, so this is not fatal.
	c.statementsInto(ctx, symbol, stamp, &f)
	c.epsInto(ctx, symbol, stamp, &f)
	c.forecastInto(ctx, symbol, stamp, &f)

	if f.Shares > 0 && len(f.Equity) > 0 && f.Equity[0] != 0 {
		f.BookValuePerShare = f.Equity[0] / f.Shares
	}
	if f.ReturnOnEquity == 0 && len(f.NetIncome) > 0 && len(f.Equity) > 0 && f.Equity[0] > 0 {
		f.ReturnOnEquity = f.NetIncome[0] / f.Equity[0]
	}
	return f, nil
}

func (c *Client) summaryInto(ctx context.Context, symbol string, price float64, stamp string, f *market.Fundamentals) error {
	body, err := c.get(ctx,
		fmt.Sprintf("%s/api/quote/%s/summary?assetclass=stocks", c.BaseURL, symbol),
		"summary:"+symbol+":"+stamp)
	if err != nil {
		return err
	}
	var resp struct {
		Data struct {
			SummaryData struct {
				Sector             labelled `json:"Sector"`
				Industry           labelled `json:"Industry"`
				MarketCap          labelled `json:"MarketCap"`
				AnnualizedDividend labelled `json:"AnnualizedDividend"`
			} `json:"summaryData"`
		} `json:"data"`
		Status apiStatus `json:"status"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("nasdaq: decode summary %s: %w", symbol, err)
	}
	if err := resp.Status.err(); err != nil {
		return err
	}
	s := resp.Data.SummaryData
	f.Sector, f.Industry = s.Sector.Value, s.Industry.Value
	f.MarketCap = num(s.MarketCap.Value)
	f.DividendPerShare = num(s.AnnualizedDividend.Value)
	if price > 0 && f.MarketCap > 0 {
		// Shares outstanding is not published here, but it is exactly the
		// capitalisation divided by the price, and that is the figure the
		// per-share conversions need.
		f.Shares = f.MarketCap / price
	}
	return nil
}

func (c *Client) statementsInto(ctx context.Context, symbol, stamp string, f *market.Fundamentals) {
	body, err := c.get(ctx,
		fmt.Sprintf("%s/api/company/%s/financials?frequency=1", c.BaseURL, symbol),
		"financials:"+symbol+":"+stamp)
	if err != nil {
		return
	}
	var resp struct {
		Data struct {
			Income  table `json:"incomeStatementTable"`
			Balance table `json:"balanceSheetTable"`
			Cash    table `json:"cashFlowTable"`
			Ratios  table `json:"financialRatiosTable"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return
	}
	d := resp.Data
	f.FiscalEnds = d.Income.periods()
	f.Revenue = d.Income.series("Total Revenue")
	f.NetIncome = d.Income.series("Net Income")
	f.Equity = d.Balance.series("Total Equity")
	f.Cash = first(d.Balance.series("Cash and Cash Equivalents"))
	f.Debt = first(d.Balance.series("Long-Term Debt")) +
		first(d.Balance.series("Short-Term Debt / Current Portion of Long-Term Debt"))

	// Free cash flow is operating cash less what it costs to stay in
	// business. Capital expenditure is reported negative, so it adds.
	ocf := d.Cash.series("Net Cash Flow-Operating")
	capex := d.Cash.series("Capital Expenditures")
	for i := range ocf {
		fcf := ocf[i]
		if i < len(capex) {
			fcf += capex[i]
		}
		f.FreeCashFlow = append(f.FreeCashFlow, fcf)
	}
	// The ratio table is quoted in percent and is not in thousands, so it
	// needs its own reader rather than the statement one.
	f.OperatingMargin = first(d.Ratios.ratios("Operating Margin")) / 100
	f.ReturnOnEquity = first(d.Ratios.ratios("After Tax ROE")) / 100
}

func (c *Client) epsInto(ctx context.Context, symbol, stamp string, f *market.Fundamentals) {
	body, err := c.get(ctx,
		fmt.Sprintf("%s/api/quote/%s/eps", c.BaseURL, symbol),
		"eps:"+symbol+":"+stamp)
	if err != nil {
		return
	}
	var resp struct {
		Data struct {
			EarningsPerShare []struct {
				Type     string  `json:"type"`
				Period   string  `json:"period"`
				Earnings float64 `json:"earnings"`
			} `json:"earningsPerShare"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return
	}
	for _, r := range resp.Data.EarningsPerShare {
		if !strings.EqualFold(r.Type, "PreviousQuarter") {
			continue
		}
		end, err := time.Parse("Jan 2006", r.Period)
		if err != nil {
			continue
		}
		f.QuarterlyEPS = append(f.QuarterlyEPS, market.QuarterEPS{
			Period: r.Period, End: end, Reported: r.Earnings,
		})
	}
	// The trailing multiple is built on the last four reported quarters.
	n := len(f.QuarterlyEPS)
	for i := max(0, n-4); i < n; i++ {
		f.TrailingEPS += f.QuarterlyEPS[i].Reported
	}
}

func (c *Client) forecastInto(ctx context.Context, symbol, stamp string, f *market.Fundamentals) {
	body, err := c.get(ctx,
		fmt.Sprintf("%s/api/analyst/%s/earnings-forecast", c.BaseURL, symbol),
		"forecast:"+symbol+":"+stamp)
	if err != nil {
		return
	}
	var resp struct {
		Data struct {
			YearlyForecast struct {
				Rows []struct {
					ConsensusEPSForecast float64 `json:"consensusEPSForecast"`
					HighEPSForecast      float64 `json:"highEPSForecast"`
					LowEPSForecast       float64 `json:"lowEPSForecast"`
					NoOfEstimates        int     `json:"noOfEstimates"`
				} `json:"rows"`
			} `json:"yearlyForecast"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return
	}
	for i, r := range resp.Data.YearlyForecast.Rows {
		if r.ConsensusEPSForecast == 0 {
			continue
		}
		f.ForwardEPS = append(f.ForwardEPS, r.ConsensusEPSForecast)
		if i == 0 {
			f.EPSEstimates = r.NoOfEstimates
			if r.ConsensusEPSForecast != 0 {
				f.EPSDispersion = (r.HighEPSForecast - r.LowEPSForecast) / r.ConsensusEPSForecast
			}
		}
	}
}

// ---------------------------------------------------------------- parsing

type labelled struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// table is Nasdaq's shape for a statement: a header row naming the periods
// and value rows whose first column is the line item.
type table struct {
	Headers map[string]string   `json:"headers"`
	Rows    []map[string]string `json:"rows"`
}

func (t table) periods() []string {
	var out []string
	for i := 2; ; i++ {
		v, ok := t.Headers[fmt.Sprintf("value%d", i)]
		if !ok || v == "" {
			return out
		}
		out = append(out, v)
	}
}

// series returns one line item across the reported periods, most recent
// first. A missing or placeholder cell ends the series rather than being
// read as zero, which would otherwise look like a real collapse.
// ratios reads a line from the ratio table, which is quoted in percent
// and in units rather than thousands.
func (t table) ratios(label string) []float64 {
	return t.read(label, 1)
}

// series reads a statement line, which is reported in thousands.
func (t table) series(label string) []float64 {
	return t.read(label, 1000)
}

func (t table) read(label string, scale float64) []float64 {
	for _, row := range t.Rows {
		if !strings.EqualFold(strings.TrimSpace(row["value1"]), label) {
			continue
		}
		var out []float64
		for i := 2; ; i++ {
			cell, ok := row[fmt.Sprintf("value%d", i)]
			if !ok {
				break
			}
			cell = strings.TrimSpace(cell)
			if cell == "" || cell == "--" {
				break
			}
			out = append(out, num(cell)*scale)
		}
		return out
	}
	return nil
}

func first(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	return xs[0]
}

// num reads Nasdaq's presentation format: a currency symbol, thousands
// separators, a trailing percent, and parentheses for negatives. It does
// no scaling -- the statements are quoted in thousands and say so at the
// call site, while the summary figures are already absolute.
func num(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "--" || s == "N/A" || s == "NA" {
		return 0
	}
	negative := strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")
	repl := strings.NewReplacer("$", "", ",", "", "(", "", ")", "", "%", "", " ", "")
	v, err := strconv.ParseFloat(repl.Replace(s), 64)
	if err != nil {
		return 0
	}
	if negative {
		v = -v
	}
	return v
}
