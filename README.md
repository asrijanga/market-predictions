# market-predictions

> **This is an experiment, built for fun.** It is not financial advice, it
> is not a trading tool, and it must not be used for any financial benefit.
> The numbers it prints come from a toy model over public data, and nothing
> it says should be acted on with real money. Treat it as a curiosity.

`mktpredict` is a Go command-line tool for finding and timing call-option
trades on US stocks, with a browser front end. Six commands:

| Command | What it does |
| --- | --- |
| `scan` | Ranks the largest liquid US stocks as call candidates from six months of price history. |
| `pack SYMBOL` | Builds a one-year data pack for one stock: prices, trend statistics, the live option chain with implied volatility, earnings date, SEC filings and headlines. |
| `analyze SYMBOL` | Runs the statistical model over that pack and answers whether the stock is bullish or bearish and why, where it is likely to be next quarter and next year, and when to buy calls. |
| `serve` | Serves the same analysis to a browser front end: a terminal-styled page that takes one ticker at a time. |
| `build-site` | Renders that front end plus precomputed analyses into a directory any static host can serve, which is how it goes on GitHub Pages. |
| `cache` | Reports on, or prunes, the DuckDB database of computed analyses. |

Everything runs locally against public data endpoints (Nasdaq, Google News
RSS, SEC EDGAR). No API keys, no accounts, and no network calls beyond
fetching that data.

The only dependency is DuckDB, which stores computed analyses so a repeated
question is instant. Its Go driver uses cgo, so building needs a C
toolchain and `CGO_ENABLED=0` builds are not available.

## Install

```sh
go install github.com/asrijanga/market-predictions/cmd/mktpredict@latest
```

or from a checkout:

```sh
go build -o mktpredict ./cmd/mktpredict
```

Both need a C compiler on the path, for DuckDB. The `Dockerfile` has a
working toolchain if you would rather not install one.

## Quick start

```sh
# 1. Find candidates: top 15 call setups for the November 2026 monthly expiry
mktpredict scan -expiry 2026-11

# 2. Deep-dive one of them: data pack only
export SEC_CONTACT_EMAIL=you@example.com   # the SEC requires a contact in the User-Agent
mktpredict pack AAPL

# 3. Model when to buy
mktpredict analyze AAPL

# 4. Or use the browser front end, which answers one ticker at a time
mktpredict serve
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

Type a ticker, press return. The page answers and waits for the next one,
and repeat questions come back from the database in milliseconds.

The interface keeps the terminal's character — monospace, phosphor green on
a dark ground, a prompt and a blinking caret — but it is an ordinary
document, not a simulated tube. That distinction is the whole design:

* **Everything is real text.** The reading is headings, paragraphs and
  lists, so it reflows to the width it is given, scales with the reader's
  font settings, can be selected, searched and read aloud, and survives a
  phone held in one hand. An earlier version painted a fixed 62×30
  character grid onto a curved WebGL texture, which could do none of those
  things and was cut off down both sides on any narrow screen.
* **The numbers are shaped to be read.** A diverging meter puts the score
  against neutral, each horizon shows its probability and its 80% band with
  today's price and the median outcome marked on it, and every signal
  carries its own reading and its weight in the total.
* **Waiting is not a spinner.** The server streams its real stages over
  server-sent events, so the page names the stage it is on: fitting the
  volatility model, decomposing the surface, simulating paths, ranking
  plans. The published site replays the same list, because those are the
  steps that produced the numbers it is about to show.
* **It is addressable.** Every reading has a URL (`?s=NVDA`), so a result
  can be linked, bookmarked and reached with the back button.

Flags: `-addr` (localhost:8080), `-paths` (20000), `-rate` (0.04),
`-max-concurrent` (4), `-timeout` (3m), `-db` (the DuckDB file, empty to
disable), `-benchmark`, `-sec-contact`, `-no-news`, and the cache flags. Symbols are validated against a strict
pattern before any work starts, and analyses are bounded by a semaphore so a
page left reloading cannot spawn unbounded work.

The front end is four files — `index.html`, `style.css`, `app.js` and
`report.js` — embedded in the binary with `go:embed`. There is no framework,
no build step, no package manager and nothing fetched from a third party at
runtime.

## Running it as a service

Nothing is computed at deploy time. The published page carries the address
of this server and asks it when a reader asks for a symbol, so publishing
is a file copy that takes seconds, and a recalculation is a request rather
than a release.

`fly.toml` and `.github/workflows/deploy.yml` deploy `serve` to Fly. The
only manual step is a token: on fly.io, choose your organisation from the
dropdown, click **Tokens**, create an org-scoped one, and save it as a
`FLY_API_TOKEN` repository secret. It has to be org-scoped rather than
app-scoped because the workflow creates the app and its volume on the first
run, so there is nothing to set up in the dashboard by hand.

After that the workflow ships on every push that touches the server, or on
demand from the Actions tab. Without the secret it warns and skips rather
than failing, and after a deploy it polls the public URL until it answers
before reporting success.

Fly app names are globally unique. If the first deploy reports that the
name is taken, change `app` in `fly.toml` and push again.

Two constraints shape the machine definition, and both are worth keeping in
mind before changing it:

* **One machine, not several.** DuckDB takes a single writer and no readers
  beside it, so a second instance pointed at the same volume fails to open
  the database. The app scales up, not out.
* **Memory follows concurrency.** One analysis peaks around 300MB, so the
  1GB machine runs `-max-concurrent 2`. Raise the two together or the
  fourth simultaneous request meets the OOM killer.
* **A mounted volume shadows the image's directory and arrives owned by
  root**, so `docker-entrypoint.sh` fixes the ownership while it is still
  root and drops to the app user before running anything. Without it the
  server cannot create its database, exits, and the deploy fails on health
  checks with nothing in flyctl's output to say why.

### How long an answer is kept

`freshFor` decides, and it follows the market rather than a round number.
Every statistic in the verdict but two is built from daily closing bars,
which do not move until the session ends; the exceptions are the spot price
and the live option chain, and they only move while the market is open. So
an entry computed during the session is served for fifteen minutes, and one
computed after the close is served until the cache key itself rolls over,
because that key carries the market date. Recomputing is lazy, so a short
window costs nothing when nobody is asking.

Eviction is least-recently-used: `Get` stamps `last_used`, and the server
trims to `-cache-entries` at startup and hourly. The database is the only
thing on the volume that grows without a natural bound, because a public
server is asked about symbols nobody asks about twice.

### Rate limiting

The endpoint is open to anyone and one miss costs eight seconds of CPU plus
a round of requests to Nasdaq, who rate-limit by IP and will throttle this
whole server for one visitor's script. `-rate-burst` and `-rate-refill` are
a per-address token bucket: five new analyses at once, then one every two
minutes.

Only computation is rationed. A symbol already in the store is served
without taking a token, so a visitor who has spent their budget can still
read everything that is there — the limit protects the CPU and the upstream
provider, and a cache hit touches neither. Behind Fly every visitor shares
the proxy's address, so the client is identified by `Fly-Client-IP`, then
`X-Forwarded-For`, then the socket. Those headers are client-settable in
principle, which makes this a fair-use measure rather than a security
boundary.

The volume also carries the HTTP response cache (`XDG_CACHE_HOME=/data`),
which matters more than its size suggests: Nasdaq rate-limits by IP and
treats datacentre ranges less kindly than residential ones, so losing the
cache on each deploy would mean refetching a year of bars and a full option
chain for every symbol.

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

`serve` and `build-site` read and write it. The `analyze` subcommand does
not: it always recomputes, because a one-off run from a terminal is usually
asking for a fresh answer. DuckDB allows a single writer and no readers
beside it, so `cache` opens read-only and reports the holder rather than a
driver error if the server is running.

The database defaults to `analyses.duckdb` in the cache directory and holds
two tables: `analyses`, the cache itself, and `requests`, an anonymous count
of questions and cache hits. It never leaves the machine it is written on.

Bumping `model.Version` invalidates every cached answer, so any change that
alters the output for the same inputs must bump it. DuckDB's Go driver needs
cgo, so builds with `CGO_ENABLED=0` and simple cross-compilation are no
longer available; the `Dockerfile` accounts for this.

## Personal data

The application collects none. It asks for a ticker symbol and nothing else:
no account, no address, no cookie, no analytics, and no third-party script.
The server logs a failed symbol and its reason, never a request's origin, and
the front end stores nothing in the browser.

The `requests` table in the database counts questions so the cache hit rate
can be measured. It holds a timestamp, a symbol and whether the cache
answered, which describes how the machine is used without describing who
used it.

An earlier version asked for an email address before running an analysis and
recorded it with each question. Opening a database written by that version
drops the old request log, so those addresses are erased rather than left in
a file nobody looks at. The cache of analyses is untouched.

The one address in the system belongs to whoever runs it: `-sec-contact`, or
`SEC_CONTACT_EMAIL`, which the SEC requires in the User-Agent of automated
requests to EDGAR. It is sent to the SEC, it identifies the operator rather
than any user, and leaving it unset simply skips filings.

## Hosting it on GitHub Pages

Pages serves static files only, so there is no Go process to run the model
there. The data providers make that worse: neither Nasdaq nor the news feed
sends cross-origin headers, so a browser on `github.io` cannot fetch them
either, with or without a backend.

So the analysis runs ahead of time and the site serves the answers:

```sh
mktpredict build-site -out dist -top 500              # the published default
mktpredict build-site -out dist -top 640 -workers 6   # ask for more, to land ~500
mktpredict build-site -out dist -symbols AAPL,NVDA    # or choose your own
```

That writes the front end, one `data/SYMBOL.json` per analysis, a
`data/index.json` describing the set, and `.nojekyll` so Pages does not run
the output through Jekyll.

`-top N` asks the screener for the N largest US-listed stocks, and fewer
than N are published: about a fifth are dropped for having too little price
history, no listed options, or no call strikes that clear the liquidity
filters. A real run of 640 published 487, at 2.7 MB in total. Publishing a
name with nothing behind it would be worse than leaving it out, so the
count is what survived rather than what was asked for.

A cold build of that size takes about a quarter of an hour at six workers.
With the database warm it is seconds, because only symbols whose market day
has moved on are recomputed.

A published site answers for the symbols it holds and says so plainly about
the rest. Ask it for something outside the set and it replies `I DON'T KNOW`,
suggests near matches for a mistyped ticker, and offers `LIST` to page
through what it does have. The live server has no such limit: it analyses
whatever you type.

`.github/workflows/pages.yml` runs it on every push to `main`, on a weekday
schedule after the US close, and on demand with a symbol list or a different
`-top`. No secrets are needed: the model reads prices, options and the
earnings date, none of which require a key, and the site build skips
headlines and filings because they feed the written brief rather than the
model.

The workflow enables Pages itself, and checks it can before starting the
build rather than after, so a destination problem fails in seconds instead
of a quarter of an hour. **Pages on a private repository requires a paid
plan**; on a free plan it is available only for public repositories. Where
the plan does not allow it the run stops at that first step with:

```
Get Pages site failed / Resource not accessible by integration
```

which means the repository is private on a plan without Pages, not that
anything is misconfigured. Make the repository public, upgrade the plan, or
host the live server instead.

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
| **GitHub Pages** | The published set, with no server to run. Answers only for symbols the workflow computed, and says `I DON'T KNOW` for the rest. | Free for a public repository; a private one needs a paid plan |
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

Measured, not estimated:

| Operation | Cold | Warm |
| --- | --- | --- |
| `pack` for one symbol | 3 to 7s | under 50 ms |
| The model inside `analyze` | about 3s | n/a, `analyze` does not use the database |
| One analysis over the API | 2.9s | 12ms |
| `scan` over 150 names | 15 to 20s | about 2.5s |
| `build-site` for 487 symbols | about 15 min at six workers | seconds |

* The fetches dominate everything cold. A `pack` makes six concurrent
  requests, and `scan` is bound by Nasdaq's per-request latency rather than
  by CPU: raise `-concurrency` if the API tolerates it.
* The model's three seconds are two year-long simulations for the forecast,
  seven shorter ones for the drift sensitivity, and roughly two thousand
  priced plans, spread across CPUs. The indicator maths either side of that
  is single-pass over float slices with closed-form regression sums, and
  costs milliseconds even over hundreds of symbols.
* Two caches sit behind these numbers and they are different things. HTTP
  responses are cached on disk by request date, which saves the fetch;
  `-no-cache` forces a refresh intraday. Whole analyses are cached in
  DuckDB, which saves the computation as well.

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
holidays.

None of this is investment advice. It is an experiment for fun, and it must
not be used for any financial benefit.

## Development

```sh
go vet ./... && go test ./...
```
