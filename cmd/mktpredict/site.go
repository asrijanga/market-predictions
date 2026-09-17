package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/asrijanga/market-predictions/internal/cache"
	"github.com/asrijanga/market-predictions/internal/model"
	"github.com/asrijanga/market-predictions/internal/nasdaq"
	"github.com/asrijanga/market-predictions/internal/pack"
	"github.com/asrijanga/market-predictions/internal/store"
	"github.com/asrijanga/market-predictions/internal/universe"
	"github.com/asrijanga/market-predictions/internal/web"
)

// defaultSiteSymbols are liquid, heavily optioned names that make a useful
// starting set for a published site.
var defaultSiteSymbols = []string{
	"AAPL", "MSFT", "NVDA", "AMZN", "GOOGL", "META", "TSLA", "AVGO",
	"AMD", "NFLX", "JPM", "V", "WMT", "XOM", "LLY", "COST",
	"ORCL", "CRM", "INTC", "MU", "PLTR", "UBER", "DIS", "BA",
}

// defaultStages names the pipeline for a build where every symbol came from
// the database and no stage callbacks fired.
var defaultStages = []string{
	"fetching one year of prices and options",
	"fitting GARCH(1,1) volatility model",
	"decomposing the implied volatility surface",
	"scoring directional signals",
	"simulating price paths",
	"ranking entry plans",
}

// siteIndex is what the front end reads to discover what has been
// published and when.
type siteIndex struct {
	GeneratedAt string      `json:"generatedAt"`
	AsOf        string      `json:"asOf"`
	Paths       int         `json:"paths"`
	Stages      []string    `json:"stages"`
	Symbols     []siteEntry `json:"symbols"`
}

type siteEntry struct {
	Symbol string  `json:"symbol"`
	Stance string  `json:"stance"`
	Score  float64 `json:"score"`
}

// buildSiteCommand renders the front end plus one precomputed analysis per
// symbol into a directory that any static host can serve.
//
// A static host cannot run the model, and the data providers do not send
// cross-origin headers, so the browser cannot reach them either. Publishing
// the answers is therefore the only honest way to put this on a page: the
// analysis runs here, on a schedule, and the site serves the result.
func buildSiteCommand(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("build-site", flag.ContinueOnError)
	dir := fs.String("out", "dist", "directory to write the site into")
	symbolList := fs.String("symbols", strings.Join(defaultSiteSymbols, ","), "comma-separated symbols to publish")
	top := fs.Int("top", 0, "instead of -symbols, publish the N largest stocks from the screener")
	benchmark := fs.String("benchmark", "SPY", "benchmark symbol")
	paths := fs.Int("paths", 20000, "simulated price paths per analysis")
	rate := fs.Float64("rate", 0.04, "risk-free rate")
	workers := fs.Int("workers", 4, "symbols to analyse at once")
	cacheDir := fs.String("cache-dir", defaultCacheDir(), "directory for cached API responses")
	noCache := fs.Bool("no-cache", false, "bypass the on-disk response cache")
	dbPath := fs.String("db", defaultDBPath(), "DuckDB file holding computed analyses; empty disables it")
	refresh := fs.Bool("refresh", false, "recompute every symbol even when the database already has today's answer")
	timeout := fs.Duration("timeout", 20*time.Minute, "overall timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	var responses *cache.Cache
	if !*noCache {
		var err error
		if responses, err = cache.New(*cacheDir); err != nil {
			return err
		}
	}
	nq := nasdaq.NewClient(8, responses)
	now := marketNow()

	symbols, err := siteSymbols(ctx, nq, *symbolList, *top, now)
	if err != nil {
		return err
	}
	if err := web.WriteStatic(*dir); err != nil {
		return fmt.Errorf("copy front end: %w", err)
	}
	dataDir := filepath.Join(*dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}

	// Headlines and filings are part of the written brief, not of the
	// model, so the site build skips them and stays fast.
	sources := pack.Sources{Nasdaq: nq}

	db, err := openStore(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	var (
		mu      sync.Mutex
		entries []siteEntry
		stages  []string
		failed  []string
	)
	sem := make(chan struct{}, max(1, *workers))
	var wg sync.WaitGroup
	for _, symbol := range symbols {
		wg.Add(1)
		sem <- struct{}{}
		go func(symbol string) {
			defer wg.Done()
			defer func() { <-sem }()

			key := store.Key{
				Symbol: symbol, AsOf: now, Paths: *paths,
				Seed: model.Defaults().Seed, ModelVersion: model.Version,
			}
			var (
				body   []byte
				view   *web.View
				seen   []string
				cached bool
			)
			if !*refresh {
				if stored, ok, err := db.Get(ctx, key); err != nil {
					log.Printf("cache read %s: %v", symbol, err)
				} else if ok {
					var v web.View
					if err := json.Unmarshal(stored, &v); err == nil {
						body, view, cached = stored, &v, true
					}
				}
			}
			started := time.Now()
			if view == nil {
				var err error
				view, err = analyseForSite(ctx, sources, symbol, *benchmark, *paths, *rate, now, func(stage string) {
					seen = append(seen, stage)
				})
				if err != nil {
					mu.Lock()
					log.Printf("skip %s: %v", symbol, err)
					failed = append(failed, symbol)
					mu.Unlock()
					return
				}
				if body, err = json.Marshal(view); err != nil {
					mu.Lock()
					failed = append(failed, symbol)
					mu.Unlock()
					return
				}
				if err := db.Put(ctx, key, body, time.Since(started)); err != nil {
					log.Printf("cache write %s: %v", symbol, err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if err := os.WriteFile(filepath.Join(dataDir, symbol+".json"), body, 0o644); err != nil {
				log.Printf("write %s: %v", symbol, err)
				failed = append(failed, symbol)
				return
			}
			entries = append(entries, siteEntry{Symbol: symbol, Stance: view.Stance, Score: view.Score})
			if len(seen) > len(stages) {
				stages = seen
			}
			from := "computed"
			if cached {
				from = "cached"
			}
			log.Printf("%-6s %-18s %+.2f  %s", symbol, view.Stance, view.Score, from)
		}(symbol)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("no symbols could be analysed")
	}
	sortEntries(entries)

	if len(stages) == 0 {
		stages = defaultStages
	}
	index := siteIndex{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		AsOf:        now.Format(time.DateOnly),
		Paths:       *paths,
		Stages:      stages,
		Symbols:     entries,
	}
	body, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dataDir, "index.json"), body, 0o644); err != nil {
		return err
	}
	// Pages would otherwise run the output through Jekyll, which skips
	// files and directories beginning with an underscore.
	if err := os.WriteFile(filepath.Join(*dir, ".nojekyll"), nil, 0o644); err != nil {
		return err
	}

	fmt.Fprintf(out, "wrote %d analyses to %s (as of %s)\n", len(entries), *dir, index.AsOf)
	if len(failed) > 0 {
		fmt.Fprintf(out, "skipped: %s\n", strings.Join(failed, ", "))
	}
	return nil
}

func analyseForSite(ctx context.Context, sources pack.Sources, symbol, benchmark string, paths int, rate float64, now time.Time, stage func(string)) (*web.View, error) {
	stage("fetching one year of prices and options")
	p, err := pack.Build(ctx, sources, symbol, pack.Options{
		Benchmark: benchmark, LookbackDays: 252, ModelDays: 756, TrendDays: 126,
		HorizonDays: 63, NewsDays: 90, MaxHeadlines: 60, RiskFreeRate: rate, Now: now,
	})
	if err != nil {
		return nil, err
	}
	opts := model.Defaults()
	opts.Paths, opts.Rate = paths, rate
	opts.Progress = func(name string, _ float64) { stage(name) }
	r, err := model.Analyze(p, opts)
	if err != nil {
		return nil, err
	}
	return web.NewView(p, r), nil
}

func siteSymbols(ctx context.Context, nq *nasdaq.Client, list string, top int, now time.Time) ([]string, error) {
	if top > 0 {
		all, err := nq.Listings(ctx, now)
		if err != nil {
			return nil, fmt.Errorf("screener: %w", err)
		}
		picks := universe.Select(all, universe.Options{Top: top, MinPrice: 5, MinDollarVolume: 20e6})
		out := make([]string, 0, len(picks))
		for _, l := range picks {
			out = append(out, l.Symbol)
		}
		return out, nil
	}
	var out []string
	for _, s := range strings.Split(list, ",") {
		if s = strings.ToUpper(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no symbols requested")
	}
	return out, nil
}

func sortEntries(entries []siteEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].Symbol < entries[j-1].Symbol; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}

// openStore opens the analysis database, or returns nil when caching is
// switched off. A nil store is safe to use.
func openStore(path string) (*store.Store, error) {
	if path == "" {
		return nil, nil
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	return store.Open(path)
}

// defaultDBPath puts the database beside the HTTP response cache.
func defaultDBPath() string {
	return filepath.Join(defaultCacheDir(), "analyses.duckdb")
}

// cacheCommand reports what the analysis database holds, and can prune it.
func cacheCommand(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("cache", flag.ContinueOnError)
	dbPath := fs.String("db", defaultDBPath(), "DuckDB file holding computed analyses")
	pruneBefore := fs.String("prune-before", "", "delete analyses for market days before this date (YYYY-MM-DD)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Reading needs no write lock, and pruning does, so only ask for what
	// this run actually needs.
	open := store.OpenReadOnly
	if *pruneBefore != "" {
		open = store.Open
	}
	db, err := open(*dbPath)
	if errors.Is(err, store.ErrLocked) {
		return fmt.Errorf("%w\n  a running `mktpredict serve` holds the database; stop it and try again", err)
	}
	if err != nil {
		return err
	}
	defer db.Close()

	if *pruneBefore != "" {
		cutoff, err := time.Parse(time.DateOnly, *pruneBefore)
		if err != nil {
			return fmt.Errorf("prune-before: %w", err)
		}
		n, err := db.Prune(ctx, cutoff)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "pruned %d analyses from before %s\n", n, *pruneBefore)
	}

	st, err := db.Stats(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "database %s\n", *dbPath)
	fmt.Fprintf(out, "  analyses      %d across %d symbols\n", st.Analyses, st.Symbols)
	if st.Analyses > 0 {
		fmt.Fprintf(out, "  market days   %s to %s\n", st.OldestAsOf, st.NewestAsOf)
		fmt.Fprintf(out, "  mean compute  %.1fs\n", st.MeanComputeMS/1000)
	}
	fmt.Fprintf(out, "  requests      %d, %d served from cache\n", st.Requests, st.CacheHits)
	return nil
}
