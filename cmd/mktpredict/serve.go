package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/asrijanga/market-predictions/internal/cache"
	"github.com/asrijanga/market-predictions/internal/edgar"
	"github.com/asrijanga/market-predictions/internal/model"
	"github.com/asrijanga/market-predictions/internal/nasdaq"
	"github.com/asrijanga/market-predictions/internal/news"
	"github.com/asrijanga/market-predictions/internal/pack"
	"github.com/asrijanga/market-predictions/internal/web"
)

// serveCommand runs the terminal front end and the streaming API behind it.
func serveCommand(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "localhost:8080", "address to listen on")
	benchmark := fs.String("benchmark", "SPY", "benchmark symbol")
	secContact := fs.String("sec-contact", envOrEmpty("SEC_CONTACT_EMAIL"), "contact email the SEC requires in the User-Agent; filings are skipped when empty")
	noNews := fs.Bool("no-news", false, "skip headline fetching")
	paths := fs.Int("paths", 20000, "simulated price paths per analysis")
	rate := fs.Float64("rate", 0.04, "risk-free rate")
	concurrency := fs.Int("max-concurrent", 4, "analyses allowed to run at once")
	timeout := fs.Duration("timeout", 3*time.Minute, "per-analysis timeout")
	cacheDir := fs.String("cache-dir", defaultCacheDir(), "directory for cached API responses")
	noCache := fs.Bool("no-cache", false, "bypass the on-disk response cache")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var store *cache.Cache
	if !*noCache {
		var err error
		if store, err = cache.New(*cacheDir); err != nil {
			return err
		}
	}
	nq := nasdaq.NewClient(8, store)
	sources := pack.Sources{Nasdaq: nq}
	if !*noNews {
		sources.News = news.NewClient(nq.HTTP, store)
	}
	if *secContact != "" {
		sources.Edgar = edgar.NewClient(nq.HTTP, store, *secContact)
	}

	srv := &web.Server{
		MaxConcurrent: *concurrency,
		Analyze: func(ctx context.Context, symbol string, progress web.Progress) (*web.View, error) {
			ctx, cancel := context.WithTimeout(ctx, *timeout)
			defer cancel()

			progress("fetching one year of prices, options and filings", 0.05)
			p, err := pack.Build(ctx, sources, symbol, pack.Options{
				Benchmark: *benchmark, LookbackDays: 252, ModelDays: 756, TrendDays: 126,
				HorizonDays: 63, NewsDays: 90, MaxHeadlines: 60, RiskFreeRate: *rate, Now: marketNow(),
			})
			if err != nil {
				return nil, err
			}
			opts := model.Defaults()
			opts.Paths, opts.Rate = *paths, *rate
			// The fetch is the first fifth of the wait; the model is the rest.
			opts.Progress = func(stage string, fraction float64) {
				progress(stage, 0.2+0.8*fraction)
			}
			r, err := model.Analyze(p, opts)
			if err != nil {
				return nil, err
			}
			return web.NewView(p, r), nil
		},
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Streaming responses stay open for the length of an analysis, so
		// the write timeout has to outlast one.
		WriteTimeout: *timeout + 30*time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdown)
	}()

	fmt.Fprintf(out, "MKTPREDICT 8000 listening on http://%s\n", *addr)
	if *secContact == "" {
		log.Print("no SEC contact email set, filings will be skipped (set -sec-contact or SEC_CONTACT_EMAIL)")
	}
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
