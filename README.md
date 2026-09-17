# market-predictions

`mktpredict` is a Go command-line tool for finding and timing call-option
trades on US stocks. It has three commands:

| Command | What it does |
| --- | --- |
| `scan` | Ranks the largest liquid US stocks as call candidates from six months of price history. |
| `pack SYMBOL` | Builds a one-year data pack for one stock: prices, trend statistics, the live option chain with implied volatility, earnings date, SEC filings and headlines. |
| `analyze SYMBOL` | Runs the statistical model over that pack and answers whether the stock is bullish or bearish and why, where it is likely to be next quarter and next year, and when to buy calls. |
| `serve` | Serves the same analysis to a browser front end: an 80s terminal, rendered in Three.js, that takes one ticker at a time. |
| `build-site` | Renders that front end plus precomputed analyses into a directory any static host can serve, which is how it goes on GitHub Pages. |
| `cache` | Reports on, or prunes, the DuckDB database of computed analyses. |

Everything runs locally against public data endpoints (Nasdaq, Google News
RSS, SEC EDGAR). There are no dependencies outside the Go standard library,
no API keys and no network calls beyond the data fetch.

## Install

```sh
go install github.com/asrijanga/market-predictions/cmd/mktpredict@latest
```

or from a checkout:

```sh
go build -o mktpredict ./cmd/mktpredict
```

## Quick start

```sh
# 1. Find candidates: top 15 call setups for the November 2026 monthly expiry
mktpredict scan -expiry 2026-11

# 2. Deep-dive one of them: data pack only
export SEC_CONTACT_EMAIL=you@example.com   # the SEC requires a contact in the User-Agent
mktpredict pack AAPL

# 3. Model when to buy
mktpredict analyze AAPL
```

## `scan`

```sh
mktpredict scan -expiry 2026-11          # November monthly expiry
mktpredict scan                          # three-month horizon, 150 stocks, 15 picks
mktpredict scan -symbols AAPL,MSFT -json # specific names, machine-readable
mktpredict scan -top 300 -n 30 -v
```

Flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `-expiry` | | Target expiry, `YYYY-MM` (third Friday) or `YYYY-MM-DD`. Sets the horizon to the trading days remaining. |
| `-horizon` | 63 | Projection horizon in trading days when `-expiry` is unset. |
| `-lookback` | 126 | Analysis window in trading days (about six months). |
| `-top` | 150 | Universe size: the largest N stocks by market cap after liquidity filters. |
| `-n` | 15 | Number of picks to print. `-all` prints everything analysed. |
| `-symbols` | | Comma-separated symbols to analyse instead of the screener universe. |
| `-benchmark` | SPY | Benchmark used for relative strength and drift shrinkage. |
| `-min-price`, `-min-dollar-volume` | 5, 20M | Universe liquidity filters. |
| `-concurrency` | 24 | Parallel HTTP requests. |
| `-json` | | Emit JSON instead of a table. |
| `-no-cache`, `-cache-dir` | | Control the on-disk response cache. |
| `-v` | | Log progress and skipped symbols to stderr. |

### Output

```
As of 2026-09-16 | lookback 126 trading days | horizon 47 trading days to Fri 2026-11-20
Benchmark SPY: trend +29.8%/yr (R² 0.71), vol 13.8%/yr | universe 150, analyzed 148, skipped 2 | 18.047s

   #    Sym   Price     6M%    1M%    R²  Vol%  RSI  DD%   Exp%  Target      ±1σ  P(up)  Score  Strike         Flags
   1    BNS   93.28   +32.7   +1.9  0.93    21   58    6   +9.4  102.04   93–112    84%   1.70      95
   2   MUFG   23.71   +40.8   +2.8  0.93    28   59    7  +11.0   26.33    23–30    81%   1.60      24
```

* **6M% / 1M%**: simple returns over the window and the last 21 days.
* **R²**: how cleanly log price fits a straight line over the window (1 = perfect trend).
* **Vol%**: annualised realised volatility. **RSI**: 14-day Wilder RSI. **DD%**: max drawdown in the window.
* **Exp% / Target**: model expected return and price at expiry. **±1σ**: one-standard-deviation price band.
* **P(up)**: model probability the stock closes above today's price at expiry.
* **Strike**: first listed strike at or above spot, using standard US increments.
* **Flags**: `below-50d`, `extended` (RSI > 70), `overbought` (RSI > 80), `deep-drawdown`, `recent-selloff`, `extreme-vol`, `volume-surge`.

### How the scan works

1. **Universe.** One request to Nasdaq's stock screener returns every US
   listing. Warrants, units, preferreds, SPACs and other non-common
   securities are dropped, share classes of one issuer collapse to the most
   liquid class, penny and thinly traded names are filtered, and the largest
   `-top` by market cap remain.
2. **Data.** Daily OHLCV for each symbol plus the benchmark is fetched from
   Nasdaq's historical endpoint with a bounded worker pool over a keep-alive
   HTTP/2 client. Transient failures retry with exponential backoff.
3. **Trend.** Over the lookback window the tool fits an ordinary
   least-squares line to log price (drift per day and R²), and computes
   realised volatility, RSI, drawdown, moving-average position and a
   20-day volume ratio.
4. **Projection.** Expected log return to expiry is `drift × days` where
   drift is the stock's fitted slope shrunk toward the benchmark (which is
   itself shrunk halfway toward an 8%/yr long-run anchor). Shrinkage runs
   from 30% to 70% of the stock's alpha depending on R², and drift is capped
   at ±100%/yr so nothing extrapolates absurdly. Volatility scales with the
   square root of time, giving the price band and P(up).
5. **Score.** The core is the horizon Sharpe-like ratio (expected log
   return divided by horizon volatility), plus a bonus for trend cleanliness
   and confirmation above the 20- and 50-day averages, minus penalties for
   overbought RSI, deep drawdowns, a sharp one-month selloff or extreme
   volatility.

## `pack`

```sh
mktpredict pack AAPL -print   # write packs/AAPL/{pack.json,pack.md} and print the brief
```

Flags: `-lookback` (252 trading days of display history), `-model-days`
(756, the estimation sample), `-trend` (126, the momentum-model window),
`-horizon` (63), `-news-days` (90), `-max-headlines` (60), `-rate`
(risk-free rate, 0.04), `-sec-contact` (or `SEC_CONTACT_EMAIL`), `-no-news`,
`-out` (default `packs`), plus the cache flags.

The pack contains:

* **Price context**: 1-year and 6-month returns against the benchmark, the
  52-week range, moving-average position, fitted trend and R², RSI,
  drawdown, 20-day and 1-year realised volatility with a percentile rank.
* **Calendar**: estimated earnings date, FOMC decision days, monthly expiries.
* **Option chain**: for every listed expiry, at-the-money call and put
  implied volatility (Black-Scholes from the bid/ask mid), the
  straddle-implied move and put/call open interest; for monthly expiries,
  each strike from at-the-money to about 10% out of the money with mid,
  spread, implied volatility, delta, breakeven and open interest.
* **Weekly closes** for 52 weeks and the last 15 daily sessions.
* **SEC filings** (8-K, 10-Q, 10-K) for the past year and up to 60 headlines
  from the past 90 days.

## `analyze`

```sh
mktpredict analyze AAPL                       # the short answer
mktpredict analyze AAPL -detail               # plus every table behind it
mktpredict analyze AAPL -drift risk-neutral   # strip the directional read out
mktpredict analyze AAPL -paths 100000 -seed 7
```

The default output is short. It says which way the stock leans and why, gives
a direction for the next quarter and the next year, and names the one trade
that follows:

```
# AAPL is STRONGLY BULLISH

Composite signal score +0.76 on a scale of -1 to +1, from 10 measurements of
trend, relative strength, volatility regime and option-market positioning.

## Direction

| Horizon | Date | Call | Probability up | Typical move | Average move | 80% range |
|---|---|---|---|---|---|---|
| Next quarter | 2026-12-15 | UP | 58% | +2.7% | +3.4% | 287 to 402 |
| Next year | 2027-09-06 | UP | 65% | +10.7% | +14.7% | 258 to 520 |

## Why strongly bullish
- Fitted trend: +68%/yr over six months, R² 0.74. Scores +1.00, weight 17%.
- Position against the 200-day average: +16.3% away from it. Scores +1.00, weight 15%.
...
## What argues against it
- RSI(14): 62. Scores -0.40, moderately bearish, weight 5%.
```

`-detail` appends the evidence: the signal table, the volatility fit and its
walk-forward scores, the implied-volatility surface, the simulated
distribution, the contract screen, every ranked entry plan and the drift
sensitivity grid.

Flags: `-detail`, `-paths` (20000), `-seed` (1), `-drift`
(`signal` | `capped` | `trend` | `risk-neutral` | `zero`), `-drift-cap`
(0.12), `-outlook-days` (252), `-top` (5), `-min-oi` (250), `-max-spread`
(0.20), plus every `pack` flag. Writes `packs/SYMBOL/report.md` and
`report.json`.

### How the model works

1. **Direction.** Ten observable signals are scored from -1 to +1 and
   weighted into a composite: the fitted trend (weighted by its R², so a
   noisy trend counts for less), position against the 200-day and 50-day
   averages, excess return over the benchmark, one-month momentum, distance
   from the 52-week high, RSI, the volatility regime's percentile, the
   option skew and the put/call open-interest ratio. The composite sets the
   stance, from strongly bearish to strongly bullish, and every signal is
   reported with its reading, score and weight, so the verdict can be
   argued with line by line.
2. **Drift.** The composite maps to an expected annual return: the
   risk-free rate plus the score's share of a 15-point equity risk premium.
   That bounds it between roughly -11% and +19%/yr. None of the signals come
   from the simulation, so setting the simulation's drift from them is not
   circular, and a strong trend cannot extrapolate into a forecast that
   flatters every long position.
3. **Volatility.** GARCH(1,1) fitted by maximum likelihood with variance
   targeting on three years of daily log returns. A walk-forward test scores
   its one-day-ahead forecasts against EWMA, a rolling 20-day window and a
   constant variance under QLIKE loss, so the report says plainly whether
   conditional modelling earns its place on this stock.
4. **The implied-volatility surface.** Listed implied variance is split by
   non-negative least squares into a diffusive part that accrues with time
   and a single bump for the earnings report:
   `IV(T)² · T = base² · T + jump² · 1{earnings before T}`. That separates
   "this stock is volatile" from "the market is paying for one binary date",
   and it yields the size of the volatility crush the moment the report
   passes, which is what decides whether to buy before or after it. A linear
   skew in log-moneyness prices strikes away from the money.
5. **Paths.** A filtered historical simulation over a full year: GARCH
   supplies the variance dynamics so volatility clusters, each shock is
   resampled from the stock's own standardized residuals so its skew and fat
   tails survive, and each earnings date in the window carries an extra draw
   scaled to the market-implied move. The quarter and year forecasts are
   read off the same paths, alongside a risk-neutral run that shows how much
   of the probability comes from the directional read rather than from the
   spread of outcomes.
6. **Plans.** Every combination of liquid contract, entry window and trigger
   (enter now, wait for a 3/5/8% dip, wait for a 2/4% breakout) is priced
   across all paths. Entry happens on the first day inside the window where
   the trigger fires, at Black-Scholes value on the forecast surface plus
   half the current spread as slippage, and the option is held to expiry.
7. **Ranking.** Plans are ranked by expected log growth of capital at a 10%
   stake, not by expected return. Ranking by expected return picks the most
   convex lottery ticket on the board every time; ranking by expected value
   rewards triggers that never fire. The growth criterion does neither, and
   it also yields the Kelly stake for each plan.
8. **Honesty about drift.** Expected returns on long calls are dominated by
   the assumed drift, so every plan is re-simulated across a grid from -20%
   to +40%/yr and reports its break-even drift: the annual return the stock
   needs for the plan to return nothing.

## `serve`

```sh
export SEC_CONTACT_EMAIL=you@example.com
mktpredict serve                    # http://localhost:8080
mktpredict serve -addr :9000 -paths 40000
```

A CRT terminal on a desk, rendered in Three.js. Click to power the tube on,
give an email address, type a ticker, press return. The machine answers and
waits for the next one, and repeat questions come back from the database in
milliseconds.

* **The screen is a real terminal.** Text is drawn into a character grid on a
  2D canvas, uploaded as a texture, and put through a shader that does what a
  cathode ray tube did to an image: barrel distortion across curved glass,
  scanlines and an aperture grille, colour separation that grows toward the
  edges, phosphor bleed on the brightest glyphs, a rolling refresh bar, mains
  flicker and a vignette. Powering on opens the picture from a horizontal
  line, the way a tube warms up.
* **Waiting is not a spinner.** The server streams its real stages over
  server-sent events, so the screen names the stage it is on: fitting the
  volatility model, decomposing the surface, simulating paths, ranking plans.
  While it waits it draws what the server is doing, a fan of simulated price
  paths spreading out from today with the distribution of where they end
  piling up against the right edge as the run proceeds.
* **The room responds.** The screen is a light source, so the case, the
  keyboard and the desk are lit by whatever the tube is showing, and the glow
  lifts while an analysis runs. Drag to look around, scroll to lean in.

Flags: `-addr` (localhost:8080), `-paths` (20000), `-rate` (0.04),
`-max-concurrent` (4), `-timeout` (3m), `-db` (the DuckDB file, empty to
disable), `-benchmark`, `-sec-contact`, `-no-news`, and the cache flags. Symbols are validated against a strict
pattern before any work starts, and analyses are bounded by a semaphore so a
page left reloading cannot spawn unbounded work.

Three.js is vendored under `internal/web/static/vendor`, and the whole front
end is embedded in the binary, so `serve` needs no build step, no package
manager and no third-party runtime dependency.

## Caching computed analyses

An analysis costs a data fetch plus a few seconds of simulation, and the
answer only changes when the market day does. Results therefore go into a
DuckDB database keyed on everything that could change them: the symbol, the
market day, the path count, the seed and the model version. A repeat
question is answered from the file; a new trading day, a different path
count or a changed model all miss and recompute.

| | Cold | From the database |
| --- | --- | --- |
| One analysis over the API | 2.9s | 12ms |
| A three-symbol site build | 4.4s | 34ms |

```sh
mktpredict cache                            # what is stored, and the hit rate
mktpredict cache -prune-before 2026-09-01   # drop old market days
mktpredict serve -db ""                     # or run without a database
mktpredict build-site -refresh              # recompute even on a hit
```

The database defaults to `analyses.duckdb` in the cache directory and holds
two tables: `analyses`, the cache itself, and `requests`, a log of which
address asked for which symbol and whether it was served from the cache. It
never leaves the machine it is written on.

Bumping `model.Version` invalidates every cached answer, so any change that
alters the output for the same inputs must bump it. DuckDB's Go driver needs
cgo, so builds with `CGO_ENABLED=0` and simple cross-compilation are no
longer available; the `Dockerfile` accounts for this.

## Asking for an email

The front end asks for an email address before it will run anything, and the
API rejects a request without a well-formed one. The address is remembered in
the browser so it is asked for once, typing `EMAIL` at the prompt changes it,
and each request is recorded in the `requests` table with the symbol and
whether the cache answered it. Nothing is sent anywhere.

On a published static site there is no server to record anything, so the
address is only kept in the browser.

## Hosting it on GitHub Pages

Pages serves static files only, so there is no Go process to run the model
there. The data providers make that worse: neither Nasdaq nor the news feed
sends cross-origin headers, so a browser on `github.io` cannot fetch them
either, with or without a backend.

So the analysis runs ahead of time and the site serves the answers:

```sh
mktpredict build-site -out dist                      # the default symbol set
mktpredict build-site -out dist -symbols AAPL,NVDA   # or choose your own
mktpredict build-site -out dist -top 40              # or the 40 largest stocks
```

That writes the front end, one `data/SYMBOL.json` per analysis, a
`data/index.json` describing the set, and `.nojekyll` so Pages does not run
the output through Jekyll. Six symbols take about twelve seconds and 760 KB.

`.github/workflows/pages.yml` runs it on every push to `main`, on a weekday
schedule after the US close, and on demand with a symbol list. To turn it on,
set **Settings → Pages → Source** to **GitHub Actions** once. No secrets are
needed: the model reads prices, options and the earnings date, none of which
require a key, and the site build skips headlines and filings because they
feed the written brief rather than the model.

The page works the same either way. It looks for `data/index.json` at boot:
finding one, it reads the published set and says so on screen and under every
report; finding none, it assumes a live server and streams from
`/api/analyze`. All asset paths are relative, so a project site under
`/repo/` works without configuration.

What a published site cannot do is answer for a symbol nobody computed. Ask
for one and the terminal says so and lists what it has.

## Where to host it

| Option | What you get | What it costs |
| --- | --- | --- |
| **GitHub Pages** | The published set, free and with no server to run. Answers only for symbols the workflow computed. | Free |
| **Fly.io with a volume** | The live machine: any symbol on demand, with the DuckDB cache surviving deploys. The best fit, because the cache wants a persistent disk. | A few dollars a month |
| **A small VPS** | The same thing with more control. Run the binary under systemd behind a reverse proxy, or the container. | A few dollars a month |
| **Google Cloud Run** | Scales to zero, so it is nearly free when idle. Its filesystem is ephemeral, so the cache resets whenever an instance recycles and only helps within one. | Usage based |

The `Dockerfile` builds a container for any of the last three. It needs a
toolchain in the builder and a glibc base at runtime, because the DuckDB
driver uses cgo:

```sh
docker build -t mktpredict .
docker run -p 8080:8080 -v mktpredict-data:/data mktpredict
```

Mount a volume on `/data` to keep the analyses and the response cache across
deploys. Publishing to Pages and running a live instance are not exclusive:
the Pages site is a free, always-available snapshot, and the live instance
answers for anything else.

## Runtime

* A `pack` run is five to seven seconds cold (six concurrent fetches) and
  under 50 ms cached. The model in `analyze` adds about three seconds for
  20,000 paths: two year-long simulations for the forecast, seven shorter
  ones for the drift sensitivity, and roughly two thousand priced plans,
  spread across CPUs.
* A cold `scan` over 150 names takes roughly 15 to 20 seconds and is bound by
  Nasdaq's per-request latency, not CPU. Raise `-concurrency` if the API
  tolerates it.
* Responses are cached under the user cache directory keyed by request date,
  so repeat runs on the same day finish in well under a second. Use
  `-no-cache` to force a refresh intraday.
* All analysis is single-pass over float slices with closed-form regression
  sums. Analysing hundreds of symbols costs a few milliseconds.

## Limitations

The scan is a momentum-continuation heuristic, not a forecast of news,
earnings or macro shocks. The direction call in `analyze` is a weighted
score of price and positioning evidence: it is a considered reading of what
is observable, not a forecast of what will be announced, and its weights are
chosen rather than fitted. It sets the drift, drift dominates option
returns, and so the break-even drift is reported for every plan and every
plan is re-simulated across a grid of alternative assumptions. Forecast
bands widen with the square root of time and are wide at one year by
construction. The simulated surface is
sticky in strike space and does not respond to the path, so a plan that only
pays off through a volatility spike is not credited for one. Payoffs are
European and held to expiry, ignoring early exercise and dividends. Earnings
dates are Nasdaq/Zacks estimates until the company confirms them. FOMC dates
are a static table through 2027. Trading-day counts ignore exchange
holidays. Nothing here is investment advice.

## Development

```sh
go vet ./... && go test ./...
```
