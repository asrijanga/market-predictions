# market-predictions

`mktpredict` is a Go command-line tool that pulls six months of public daily
price history for the largest, most liquid US stocks, measures each name's
trend, and projects it forward to a chosen options expiry. It ranks the
universe by how attractive a long call looks and prints the top picks.

It has no dependencies outside the Go standard library and needs no API key.

## Install

```sh
go install github.com/asrijanga/market-predictions/cmd/mktpredict@latest
```

or from a checkout:

```sh
go build -o mktpredict ./cmd/mktpredict
```

## Usage

```sh
# Top 15 call candidates for the November 2026 monthly expiry (third Friday)
mktpredict -expiry 2026-11

# Default: three-month horizon, 150-stock universe, 15 picks
mktpredict

# Analyse specific names, machine-readable
mktpredict -symbols AAPL,MSFT,NVDA -json

# Bigger universe, more picks, progress logging
mktpredict -top 300 -n 30 -v
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

## Output

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

## How it works

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

## Runtime

* A cold run over 150 names takes roughly 15 to 20 seconds and is bound by
  Nasdaq's per-request latency, not CPU. Raise `-concurrency` if the API
  tolerates it.
* Responses are cached under the user cache directory keyed by request date,
  so repeat runs on the same day finish in well under a second. Use
  `-no-cache` to force a refresh intraday.
* All analysis is single-pass over float slices with closed-form regression
  sums. Analysing hundreds of symbols costs a few milliseconds.

## Limitations

This is a momentum-continuation heuristic, not a forecast of news, earnings
or macro shocks. It knows nothing about implied volatility, so a high
P(up) does not mean the call is cheap. Trading-day counts ignore exchange
holidays. Nothing here is investment advice.

## Development

```sh
go vet ./... && go test ./...
```
