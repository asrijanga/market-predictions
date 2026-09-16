// Package report renders analysis results as a terminal table or JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/asrijanga/market-predictions/internal/quant"
)

// Result is the complete output of one run.
type Result struct {
	AsOf         time.Time        `json:"as_of"`
	Expiry       time.Time        `json:"expiry"`
	HorizonDays  int              `json:"horizon_days"`
	LookbackDays int              `json:"lookback_days"`
	Benchmark    string           `json:"benchmark"`
	BenchStats   quant.Stats      `json:"benchmark_stats"`
	Universe     int              `json:"universe_size"`
	Analyzed     int              `json:"analyzed"`
	Skipped      []string         `json:"skipped,omitempty"`
	Elapsed      time.Duration    `json:"elapsed_ns"`
	Picks        []quant.Forecast `json:"picks"`
}

// JSON writes the result as indented JSON.
func JSON(w io.Writer, r Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Table writes a human-readable summary and ranked table.
func Table(w io.Writer, r Result) error {
	benchAnnual := r.BenchStats.Drift * quant.TradingDaysPerYear
	fmt.Fprintf(w, "As of %s | lookback %d trading days | horizon %d trading days to %s\n",
		r.AsOf.Format("2006-01-02"), r.LookbackDays, r.HorizonDays, r.Expiry.Format("Mon 2006-01-02"))
	fmt.Fprintf(w, "Benchmark %s: trend %+.1f%%/yr (R² %.2f), vol %.1f%%/yr | universe %d, analyzed %d, skipped %d | %s\n\n",
		r.Benchmark, 100*benchAnnual, r.BenchStats.R2,
		100*r.BenchStats.Vol*math.Sqrt(quant.TradingDaysPerYear), r.Universe, r.Analyzed, len(r.Skipped), r.Elapsed.Round(time.Millisecond))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "#\tSym\tPrice\t6M%\t1M%\tR²\tVol%\tRSI\tDD%\tExp%\tTarget\t±1σ\tP(up)\tScore\tStrike\tFlags\t")
	for i, f := range r.Picks {
		fmt.Fprintf(tw, "%d\t%s\t%.2f\t%+.1f\t%+.1f\t%.2f\t%.0f\t%.0f\t%.0f\t%+.1f\t%.2f\t%.0f–%.0f\t%.0f%%\t%.2f\t%g\t%s\t\n",
			i+1, f.Symbol, f.Price, 100*f.Return6M, 100*f.Return1M, f.R2, 100*f.AnnualVol, f.RSI,
			100*f.MaxDrawdown, 100*f.ExpectedReturn, f.ExpectedPrice, f.LowPrice, f.HighPrice,
			100*f.ProbUp, f.Score, f.Strike, strings.Join(f.Flags, ","))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exp% = model expected return to expiry; Target = expected price; ±1σ = one-standard-deviation price band;")
	fmt.Fprintln(w, "Strike = first listed strike at/above spot. Heuristic momentum model, not investment advice.")
	return nil
}
