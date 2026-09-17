package main

import (
	"context"
	"encoding/json"
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
	timeout := fs.Duration("timeout", 20*time.Minute, "overall timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	var store *cache.Cache
	if !*noCache {
		var err error
		if store, err = cache.New(*cacheDir); err != nil {
			return err
		}
	}
	nq := nasdaq.NewClient(8, store)
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

			var seen []string
			view, err := analyseForSite(ctx, sources, symbol, *benchmark, *paths, *rate, now, func(stage string) {
				seen = append(seen, stage)
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				log.Printf("skip %s: %v", symbol, err)
				failed = append(failed, symbol)
				return
			}
			body, err := json.Marshal(view)
			if err != nil {
				failed = append(failed, symbol)
				return
			}
			if err := os.WriteFile(filepath.Join(dataDir, symbol+".json"), body, 0o644); err != nil {
				log.Printf("write %s: %v", symbol, err)
				failed = append(failed, symbol)
				return
			}
			entries = append(entries, siteEntry{Symbol: symbol, Stance: view.Stance, Score: view.Score})
			if len(seen) > len(stages) {
				stages = seen
			}
			log.Printf("%-6s %-18s %+.2f", symbol, view.Stance, view.Score)
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
