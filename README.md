# market-predictions

`mktpredict` is a Go command-line tool for finding and timing call-option
trades on US stocks. It has three commands:

| Command | What it does |
| --- | --- |
| `scan` | Ranks the largest liquid US stocks as call candidates from six months of price history. |
| `pack SYMBOL` | Builds a one-year data pack for one stock: prices, trend statistics, the live option chain with implied volatility, earnings date, SEC filings and headlines. |
| `analyze SYMBOL` | Runs the statistical model over that pack and reports when in the next three months to buy calls, with dated entry windows, triggers and expected outcomes. |

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
mktpredict analyze AAPL                       # default: capped drift, 20k paths
mktpredict analyze AAPL -drift risk-neutral   # strip the trend assumption out
mktpredict analyze AAPL -paths 100000 -seed 7 -top 8
```

Flags: `-paths` (20000), `-seed` (1), `-drift`
(`capped` | `trend` | `risk-neutral` | `zero`), `-drift-cap` (0.12),
`-top` (5), `-min-oi` (250), `-max-spread` (0.20), plus every `pack` flag.
Writes `packs/SYMBOL/report.md` and `report.json`.

### How the model works

1. **Volatility.** A GARCH(1,1) model is fitted by maximum likelihood with
   variance targeting on three years of daily log returns, giving the
   current conditional volatility, the long-run level, and the speed a shock
   decays. A walk-forward test scores its one-day-ahead forecasts against
   EWMA, a rolling 20-day window and a constant variance under QLIKE loss,
   so the report says plainly whether the model earns its place on this
   stock.
2. **The implied-volatility surface.** Listed implied variance is split by
   non-negative least squares into a diffusive part that accrues with time
   and a single bump for the earnings report:
   `IV(T)² · T = base² · T + jump² · 1{earnings before T}`. That separates
   "this stock is volatile" from "the market is paying for one binary date",
   and it yields the size of the volatility crush the moment the report
   passes, which is what decides whether to buy before or after it. A linear
   skew in log-moneyness prices strikes away from the money.
3. **Paths.** A filtered historical simulation: GARCH supplies the variance
   dynamics so volatility clusters, each shock is resampled from the stock's
   own standardized residuals so its skew and fat tails survive, and the
   earnings day carries an extra draw scaled to the market-implied move.
4. **Plans.** Every combination of liquid contract, entry window and trigger
   (enter now, wait for a 3/5/8% dip, wait for a 2/4% breakout) is priced
   across all paths. Entry happens on the first day inside the window where
   the trigger fires, at Black-Scholes value on the forecast surface plus
   half the current spread as slippage, and the option is held to expiry.
   Each plan reports its fill probability, return if filled, median, win
   rate and expected value.
5. **Ranking.** Plans are ranked by expected log growth of capital at a 10%
   stake, not by expected return. Ranking by expected return picks the most
   convex lottery ticket on the board every time; ranking by expected value
   rewards triggers that never fire. The growth criterion does neither, and
   it also yields the Kelly stake for each plan.
6. **Honesty about drift.** Expected returns on long calls are dominated by
   the assumed drift, so the fitted trend is capped at 12%/yr by default,
   every plan is re-simulated across a grid from -20% to +40%/yr, and each
   one reports the break-even drift: the annual return the stock needs for
   the plan to return nothing. That is the number to compare against your
   own view.

## Runtime

* A `pack` run is five to seven seconds cold (six concurrent fetches) and
  under 50 ms cached. The model in `analyze` adds about 2.5 seconds for
  20,000 paths: eight simulations (one for the chosen drift, seven for the
  sensitivity grid) and roughly two thousand priced plans, spread across
  CPUs.
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
earnings or macro shocks. The model in `analyze` forecasts volatility and
prices structures under an assumed drift; it does not forecast direction,
and the drift it assumes is the weakest input by a wide margin, which is why
the break-even drift is reported for every plan. The simulated surface is
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
