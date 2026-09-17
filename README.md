# market-predictions

`mktpredict` is a Go command-line tool for finding and timing call-option
trades on US stocks. It has three commands:

| Command | What it does |
| --- | --- |
| `scan` | Ranks the largest liquid US stocks as call candidates from six months of price history. |
| `pack SYMBOL` | Builds a one-year data pack for one stock: prices, trend statistics, the live option chain with implied volatility, earnings date, SEC filings and headlines. |
| `analyze SYMBOL` | Builds the pack and asks Claude when in the next three months to buy calls, writing a report with dated entry windows. |

A Claude Code skill, `/call-timing SYMBOL`, runs the same pack through three
parallel analyst subagents instead of a single model call.

The market data comes from public endpoints (Nasdaq, Google News RSS, SEC
EDGAR) and needs no API key. Only `analyze` talks to Claude.

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

# 2. Deep-dive one of them: data pack only (no model call)
export SEC_CONTACT_EMAIL=you@example.com   # the SEC requires a contact in the User-Agent
mktpredict pack AAPL

# 3. Ask Claude when to buy
mktpredict analyze AAPL
```

## Authenticating to Claude

`analyze` picks a backend automatically (`-backend auto`), or you can force one:

| You have | Backend | How it authenticates |
| --- | --- | --- |
| A Claude subscription (Pro/Max) | `claude-code` | Shells out to the `claude` CLI in headless mode (`claude -p`). Install Claude Code, run `claude` once and log in; the CLI reuses that login. No API key involved. |
| An Anthropic API key | `api` | Set `ANTHROPIC_API_KEY`. Calls the Messages API with the official Go SDK, billed per token. |
| An `ant auth login` profile | `api` | The SDK reads the profile automatically when no key is set. |

Auto-detection order: API key or token in the environment, then an `ant`
profile on disk, then a `claude` binary on `PATH`. Override with `-backend`
and `-model` (API default `claude-opus-5`, CLI default `opus`).

Both backends send the same system prompt and enforce the same JSON schema,
so reports are comparable across them.

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

## `pack` and `analyze`

```sh
mktpredict pack AAPL -print            # write packs/AAPL/{pack.json,pack.md} and print the brief
mktpredict analyze AAPL -v             # also write packs/AAPL/{report.json,report.md}
mktpredict analyze NVDA -backend api -model claude-opus-5
mktpredict analyze NVDA -backend claude-code -model opus
```

Flags: `-lookback` (252 trading days), `-trend` (126, the momentum-model
window), `-horizon` (63), `-news-days` (90), `-max-headlines` (60), `-rate`
(risk-free rate for pricing, 0.04), `-sec-contact` (or `SEC_CONTACT_EMAIL`),
`-no-news`, `-out` (default `packs`), `-timeout` (10m), plus the cache flags.

The pack contains:

* **Price context**: 1-year and 6-month returns versus the benchmark, 52-week
  range, 200/50/20-day moving-average position, fitted trend and R², RSI,
  drawdown, 20-day and 1-year realised volatility with a percentile rank, and
  the scan's momentum projection.
* **Calendar**: estimated earnings date, FOMC decision days, monthly expiries.
* **Option chain**: for every listed expiry, the ATM call and put implied
  volatility (Black-Scholes from the bid/ask mid), straddle-implied move and
  put/call open interest; for monthly expiries, each strike from ATM to about
  10% out of the money with mid, spread, IV, delta, breakeven and open interest.
* **Weekly closes** for 52 weeks and the last 15 daily sessions.
* **SEC filings** (8-K, 10-Q, 10-K) for the past year and up to 60 headlines
  from the past 90 days.

`analyze` sends the rendered brief to Claude with a fixed system prompt and a
JSON schema and writes the structured verdict: stance, thesis, up to three
entry windows (dates, trigger, expiry, strike, rationale, invalidation), an
avoid list, risks and what would change the view.

## Claude Code skill

`/call-timing AAPL` inside Claude Code builds the pack with the CLI, fans out
three subagents (technicals, catalysts, options), and writes
`packs/AAPL/report.md` with the same sections. See
`.claude/skills/call-timing/SKILL.md`.

## Runtime

* A `pack` run is five to seven seconds cold (six concurrent fetches) and
  under 50 ms cached. The model call in `analyze` typically takes one to
  three minutes.
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
earnings or macro shocks. The analysis step is a language model reading a
brief: it makes the catalysts, volatility setup and timing logic explicit and
auditable, but it does not predict prices. Earnings dates are Nasdaq/Zacks
estimates until confirmed. FOMC dates are a static table through 2027.
Trading-day counts ignore exchange holidays. Nothing here is investment
advice.

## Development

```sh
go vet ./... && go test ./...
```
