package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asrijanga/market-predictions/internal/cache"
	"github.com/asrijanga/market-predictions/internal/edgar"
	"github.com/asrijanga/market-predictions/internal/model"
	"github.com/asrijanga/market-predictions/internal/nasdaq"
	"github.com/asrijanga/market-predictions/internal/news"
	"github.com/asrijanga/market-predictions/internal/pack"
)

type deepConfig struct {
	out          string
	benchmark    string
	lookback     int
	modelDays    int
	trend        int
	horizon      int
	newsDays     int
	maxHeadlines int
	rate         float64
	secContact   string
	noNews       bool
	cacheDir     string
	noCache      bool
	timeout      time.Duration
	paths        int
	seed         uint64
	driftMode    string
	driftCap     float64
	topN         int
	minOI        int64
	maxSpread    float64
	printPack    bool
	verbose      bool
}

// packCommand implements both `pack` and `analyze`; analyze additionally
// runs the statistical model over the pack and writes report.md and
// report.json.
func packCommand(ctx context.Context, args []string, out io.Writer, analyze bool) error {
	name := "pack"
	if analyze {
		name = "analyze"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	var cfg deepConfig
	fs.StringVar(&cfg.out, "out", "packs", "directory to write packs/SYMBOL/{pack.json,pack.md,report.md}")
	fs.StringVar(&cfg.benchmark, "benchmark", "SPY", "benchmark symbol")
	fs.IntVar(&cfg.lookback, "lookback", 252, "trading days of price history to display (252 ≈ 1 year)")
	fs.IntVar(&cfg.modelDays, "model-days", 756, "trading days of history to estimate the volatility model on (756 ≈ 3 years)")
	fs.IntVar(&cfg.trend, "trend", 126, "window for the momentum model in trading days")
	fs.IntVar(&cfg.horizon, "horizon", 63, "projection horizon in trading days (63 ≈ 3 months)")
	fs.IntVar(&cfg.newsDays, "news-days", 90, "calendar days of headlines to include")
	fs.IntVar(&cfg.maxHeadlines, "max-headlines", 60, "cap on headlines in the pack")
	fs.Float64Var(&cfg.rate, "rate", 0.04, "risk-free rate for option pricing")
	fs.StringVar(&cfg.secContact, "sec-contact", os.Getenv("SEC_CONTACT_EMAIL"), "contact email the SEC requires in the User-Agent (env SEC_CONTACT_EMAIL); filings are skipped when empty")
	fs.BoolVar(&cfg.noNews, "no-news", false, "skip headline fetching")
	fs.StringVar(&cfg.cacheDir, "cache-dir", defaultCacheDir(), "directory for cached API responses")
	fs.BoolVar(&cfg.noCache, "no-cache", false, "bypass the on-disk response cache")
	fs.DurationVar(&cfg.timeout, "timeout", 3*time.Minute, "overall timeout")
	fs.BoolVar(&cfg.printPack, "print", false, "also print pack.md to stdout")
	fs.BoolVar(&cfg.verbose, "v", false, "log progress to stderr")
	if analyze {
		fs.IntVar(&cfg.paths, "paths", 20000, "simulated price paths")
		fs.Uint64Var(&cfg.seed, "seed", 1, "random seed, for reproducible runs")
		fs.StringVar(&cfg.driftMode, "drift", model.DriftCapped, "capped | trend | risk-neutral | zero: the expected drift of the simulation")
		fs.Float64Var(&cfg.driftCap, "drift-cap", 0.12, "cap on the annualised drift under -drift capped")
		fs.IntVar(&cfg.topN, "top", 5, "entry windows to report")
		fs.Int64Var(&cfg.minOI, "min-oi", 250, "minimum open interest for a contract to be considered")
		fs.Float64Var(&cfg.maxSpread, "max-spread", 0.20, "maximum bid-ask spread as a fraction of mid")
	}
	// Accept the symbol before or after the flags: "pack AAPL -v" reads
	// more naturally than "pack -v AAPL", which is what flag alone allows.
	var symbol string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		symbol, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if symbol == "" && fs.NArg() == 1 {
		symbol = fs.Arg(0)
	} else if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("%s: exactly one SYMBOL is required", name)
	}
	if symbol == "" {
		fs.Usage()
		return fmt.Errorf("%s: SYMBOL is required", name)
	}
	symbol = strings.ToUpper(symbol)
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	var store *cache.Cache
	if !cfg.noCache {
		var err error
		if store, err = cache.New(cfg.cacheDir); err != nil {
			return err
		}
	}
	nq := nasdaq.NewClient(8, store)
	src := pack.Sources{Nasdaq: nq}
	if !cfg.noNews {
		src.News = news.NewClient(nq.HTTP, store)
	}
	if cfg.secContact != "" {
		src.Edgar = edgar.NewClient(nq.HTTP, store, cfg.secContact)
	}

	start := time.Now()
	p, err := pack.Build(ctx, src, symbol, pack.Options{
		Benchmark: cfg.benchmark, LookbackDays: cfg.lookback, ModelDays: cfg.modelDays, TrendDays: cfg.trend, HorizonDays: cfg.horizon,
		NewsDays: cfg.newsDays, MaxHeadlines: cfg.maxHeadlines, RiskFreeRate: cfg.rate, Now: marketNow(),
	})
	if err != nil {
		return err
	}
	dir, err := pack.Write(cfg.out, p)
	if err != nil {
		return err
	}
	if cfg.verbose || !analyze {
		log.Printf("pack: %d bars, %d expiries, %d headlines, %d filings in %s -> %s", len(p.Bars), len(p.Expiries), len(p.Headlines), len(p.Filings), time.Since(start).Round(time.Millisecond), dir)
		for _, w := range p.Warnings {
			log.Printf("warning: %s", w)
		}
	}
	md := pack.Render(p)
	if cfg.printPack {
		fmt.Fprint(out, md)
	}
	if !analyze {
		return nil
	}

	opts := model.Defaults()
	opts.Paths, opts.Seed, opts.DriftMode = cfg.paths, cfg.seed, cfg.driftMode
	opts.TopN, opts.MinOI, opts.MaxSpread, opts.Rate = cfg.topN, cfg.minOI, cfg.maxSpread, cfg.rate
	if cfg.driftCap > 0 {
		opts.DriftCap = cfg.driftCap
	}
	res, err := model.Analyze(p, opts)
	if err != nil {
		return err
	}
	if cfg.verbose {
		log.Printf("model: GARCH persistence %.3f, base vol %.0f%%, implied earnings move %.1f%%, %d contracts, %s",
			res.GARCH.Persistence(), 100*res.IV.BaseVol, 100*res.ImpliedMove, len(res.Contracts), res.Elapsed)
	}
	reportMD := model.Render(res)
	if err := writeReport(dir, res, reportMD); err != nil {
		return err
	}
	fmt.Fprint(out, reportMD)
	return nil
}

func writeReport(dir string, rep *model.Result, md string) error {
	js, err := jsonIndent(rep)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), js, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(md), 0o644)
}
