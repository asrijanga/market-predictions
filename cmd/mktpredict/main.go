// Command mktpredict finds and times call-option trades on US stocks.
//
//	mktpredict scan [flags]           rank a universe of stocks as call candidates
//	mktpredict pack SYMBOL [flags]    build a one-year data pack for one stock
//	mktpredict analyze SYMBOL [flags] build the pack and model the entry timing
//	mktpredict serve [flags]          run the terminal front end on localhost
//	mktpredict build-site [flags]     render the front end and precomputed data for static hosting
//
// Run any subcommand with -h for its flags.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/asrijanga/market-predictions/internal/cache"
	"github.com/asrijanga/market-predictions/internal/market"
	"github.com/asrijanga/market-predictions/internal/nasdaq"
	"github.com/asrijanga/market-predictions/internal/quant"
	"github.com/asrijanga/market-predictions/internal/report"
	"github.com/asrijanga/market-predictions/internal/universe"
)

const usage = `usage: mktpredict <command> [flags]

Commands:
  scan      rank the largest liquid US stocks as call candidates (default)
  pack      build a one-year data pack (prices, options, news, filings) for one symbol
  analyze   model when to buy calls on one symbol over the next three months
  serve     run the browser front end: a terminal that answers one symbol at a time
  build-site  render the front end plus precomputed analyses for a static host
  cache     report on, or prune, the stored analyses

Run "mktpredict <command> -h" for flags.
`

func main() {
	log.SetFlags(0)
	log.SetPrefix("mktpredict: ")

	args := os.Args[1:]
	cmd := "scan"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var err error
	switch cmd {
	case "scan":
		err = scanCommand(ctx, args, os.Stdout)
	case "pack":
		err = packCommand(ctx, args, os.Stdout, false)
	case "analyze":
		err = packCommand(ctx, args, os.Stdout, true)
	case "serve":
		err = serveCommand(ctx, args, os.Stdout)
	case "build-site":
		err = buildSiteCommand(ctx, args, os.Stdout)
	case "cache":
		err = cacheCommand(ctx, args, os.Stdout)
	case "help", "-h", "--help":
		fmt.Fprint(os.Stderr, usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		log.Fatal(err)
	}
}

type config struct {
	symbols         string
	top             int
	picks           int
	lookback        int
	horizon         int
	expiry          string
	benchmark       string
	minPrice        float64
	minDollarVolume float64
	concurrency     int
	timeout         time.Duration
	cacheDir        string
	noCache         bool
	jsonOut         bool
	all             bool
	verbose         bool
}

func scanCommand(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	var cfg config
	fs.StringVar(&cfg.symbols, "symbols", "", "comma-separated symbols to analyse instead of the screener universe")
	fs.IntVar(&cfg.top, "top", 150, "universe size: largest N stocks by market cap")
	fs.IntVar(&cfg.picks, "n", 15, "number of ranked picks to print")
	fs.IntVar(&cfg.lookback, "lookback", 126, "analysis window in trading days (126 ≈ 6 months)")
	fs.IntVar(&cfg.horizon, "horizon", 63, "projection horizon in trading days (63 ≈ 3 months); ignored when -expiry is set")
	fs.StringVar(&cfg.expiry, "expiry", "", "target options expiry as YYYY-MM (third Friday) or YYYY-MM-DD")
	fs.StringVar(&cfg.benchmark, "benchmark", "SPY", "benchmark symbol for relative strength")
	fs.Float64Var(&cfg.minPrice, "min-price", 5, "minimum share price for universe membership")
	fs.Float64Var(&cfg.minDollarVolume, "min-dollar-volume", 20e6, "minimum daily dollar volume for universe membership")
	fs.IntVar(&cfg.concurrency, "concurrency", 24, "parallel HTTP requests")
	fs.DurationVar(&cfg.timeout, "timeout", 3*time.Minute, "overall run timeout")
	fs.StringVar(&cfg.cacheDir, "cache-dir", defaultCacheDir(), "directory for cached API responses")
	fs.BoolVar(&cfg.noCache, "no-cache", false, "bypass the on-disk response cache")
	fs.BoolVar(&cfg.jsonOut, "json", false, "emit JSON instead of a table")
	fs.BoolVar(&cfg.all, "all", false, "print every analysed symbol, not just the top -n")
	fs.BoolVar(&cfg.verbose, "v", false, "log progress and skipped symbols to stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	return run(ctx, cfg, out)
}

func run(ctx context.Context, cfg config, out io.Writer) error {
	start := time.Now()
	now := marketNow()

	expiry, horizon, err := resolveHorizon(cfg, now)
	if err != nil {
		return err
	}
	if cfg.lookback < 40 {
		return errors.New("-lookback must be at least 40 trading days")
	}

	var store *cache.Cache
	if !cfg.noCache {
		if store, err = cache.New(cfg.cacheDir); err != nil {
			return err
		}
	}
	client := nasdaq.NewClient(cfg.concurrency, store)

	listings, err := selectUniverse(ctx, client, cfg, now)
	if err != nil {
		return err
	}
	if cfg.verbose {
		log.Printf("universe: %d symbols; fetching %d trading days of history", len(listings), cfg.lookback)
	}

	// Fetch a little extra calendar time so holidays don't starve the window.
	from := now.AddDate(0, 0, -(cfg.lookback*7/5 + 21))
	symbols := make([]string, 0, len(listings)+1)
	symbols = append(symbols, cfg.benchmark)
	for _, l := range listings {
		symbols = append(symbols, l.Symbol)
	}
	histories, fetchErrs := fetchAll(ctx, client, symbols, from, now, cfg.concurrency)
	if err := ctx.Err(); err != nil {
		return err
	}

	benchBars := histories[0]
	if fetchErrs[0] != nil {
		return fmt.Errorf("benchmark %s: %w", cfg.benchmark, fetchErrs[0])
	}
	if len(benchBars) < cfg.lookback*4/5 {
		return fmt.Errorf("benchmark %s: only %d bars", cfg.benchmark, len(benchBars))
	}
	bench := quant.SeriesStats(quant.Closes(benchBars), cfg.lookback)

	forecasts := make([]quant.Forecast, 0, len(listings))
	var skipped []string
	for i, l := range listings {
		bars, ferr := histories[i+1], fetchErrs[i+1]
		if ferr != nil {
			skipped = append(skipped, l.Symbol)
			if cfg.verbose {
				log.Printf("skip %s: %v", l.Symbol, ferr)
			}
			continue
		}
		f, aerr := quant.Analyze(l.Symbol, bars, bench, cfg.lookback, horizon)
		if aerr != nil {
			skipped = append(skipped, l.Symbol)
			if cfg.verbose {
				log.Printf("skip %s: %v", l.Symbol, aerr)
			}
			continue
		}
		forecasts = append(forecasts, f)
	}
	if len(forecasts) == 0 {
		return errors.New("no symbols could be analysed")
	}
	quant.Rank(forecasts)
	sort.Strings(skipped)

	picks := forecasts
	if !cfg.all && cfg.picks > 0 && len(picks) > cfg.picks {
		picks = picks[:cfg.picks]
	}
	res := report.Result{
		AsOf:         now,
		Expiry:       expiry,
		HorizonDays:  horizon,
		LookbackDays: cfg.lookback,
		Benchmark:    cfg.benchmark,
		BenchStats:   bench,
		Universe:     len(listings),
		Analyzed:     len(forecasts),
		Skipped:      skipped,
		Elapsed:      time.Since(start),
		Picks:        picks,
	}
	if cfg.jsonOut {
		return report.JSON(out, res)
	}
	return report.Table(out, res)
}

// resolveHorizon derives the projection length: trading days to -expiry if
// given, otherwise -horizon with the expiry set to the matching third Friday.
func resolveHorizon(cfg config, now time.Time) (time.Time, int, error) {
	if cfg.expiry != "" {
		exp, err := quant.ParseExpiry(cfg.expiry)
		if err != nil {
			return time.Time{}, 0, err
		}
		days := quant.TradingDays(now, exp)
		if days < 5 {
			return time.Time{}, 0, fmt.Errorf("expiry %s is only %d trading days away", exp.Format(time.DateOnly), days)
		}
		return exp, days, nil
	}
	if cfg.horizon < 5 {
		return time.Time{}, 0, errors.New("-horizon must be at least 5 trading days")
	}
	// Approximate the calendar date so the header shows a concrete expiry.
	approx := now.AddDate(0, 0, cfg.horizon*7/5)
	exp := quant.ThirdFriday(approx.Year(), approx.Month())
	return exp, cfg.horizon, nil
}

func selectUniverse(ctx context.Context, client *nasdaq.Client, cfg config, now time.Time) ([]market.Listing, error) {
	if cfg.symbols != "" {
		var out []market.Listing
		for _, s := range strings.Split(cfg.symbols, ",") {
			if s = strings.ToUpper(strings.TrimSpace(s)); s != "" {
				out = append(out, market.Listing{Symbol: s})
			}
		}
		if len(out) == 0 {
			return nil, errors.New("-symbols contained no symbols")
		}
		return out, nil
	}
	all, err := client.Listings(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("screener: %w", err)
	}
	return universe.Select(all, universe.Options{
		Top:             cfg.top,
		MinPrice:        cfg.minPrice,
		MinDollarVolume: cfg.minDollarVolume,
	}), nil
}

// fetchAll downloads history for every symbol with a bounded worker pool.
// Results are positional so no locking is needed around the output slices.
func fetchAll(ctx context.Context, client *nasdaq.Client, symbols []string, from, to time.Time, concurrency int) ([][]market.Bar, []error) {
	if concurrency < 1 {
		concurrency = runtime.NumCPU()
	}
	bars := make([][]market.Bar, len(symbols))
	errs := make([]error, len(symbols))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, sym := range symbols {
		if ctx.Err() != nil {
			errs[i] = ctx.Err()
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, sym string) {
			defer wg.Done()
			defer func() { <-sem }()
			bars[i], errs[i] = client.History(ctx, sym, from, to)
		}(i, sym)
	}
	wg.Wait()
	return bars, errs
}

// marketNow returns the current date in the US market's time zone so that
// cache keys and date ranges roll over with the trading day.
func marketNow() time.Time {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	return time.Now().In(loc)
}

// envOrEmpty reads an environment variable, returning "" when unset.
func envOrEmpty(key string) string { return os.Getenv(key) }

func defaultCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "mktpredict")
}
