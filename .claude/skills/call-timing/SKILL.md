---
name: call-timing
description: Decide when to buy call options on one US stock in the next three months. Builds a one-year data pack (prices, option chain with implied vol, earnings date, SEC filings, headlines) with the Go CLI, then fans out technical, catalyst and options analysts and writes packs/SYMBOL/report.md. Use when the user names a ticker and asks when to buy calls, whether to buy calls, or for a call-timing report.
argument-hint: SYMBOL [--expiry YYYY-MM]
---

# Call timing for one stock

The user wants to know **when** in the next three months to buy calls on `$ARGUMENTS`
(first token is the symbol, uppercase it). Direction alone is not the deliverable;
every recommendation must be a dated window with a verifiable trigger, a specific
expiry and strike, and an invalidation.

## 1. Build the data pack

Run from the repository root:

```sh
go run ./cmd/mktpredict pack SYMBOL -sec-contact "${SEC_CONTACT_EMAIL:-}"
```

If the SEC contact email is empty the command still succeeds and just skips filings;
mention that gap in the report. The command writes `packs/SYMBOL/pack.md` (the brief)
and `packs/SYMBOL/pack.json` (raw). Read `pack.md` in full before delegating; if it
reports data warnings, carry them into the report's risks.

## 2. Fan out three analysts

Spawn three subagents **in parallel** (general-purpose, low effort is enough). Give
each the full path to `pack.md`, tell it to read the whole file, and to answer only
its brief in under 300 words with numbers quoted from the pack:

- **Technicals.** Trend state (fitted drift, R², moving averages, RSI), the 52-week
  range, support and resistance from the weekly closes, whether the move is extended,
  and the two or three price levels that would make a pullback entry or a breakout
  entry. Name the levels.
- **Catalysts.** What the headlines and filings say about the next quarter; the
  scheduled events (earnings, FOMC, expiries); what looks priced in; anything that
  argues for waiting. Distinguish dated facts from commentary.
- **Options.** ATM IV versus 20-day and 1-year realised vol; the term structure
  across expiries and where the earnings bump sits; the straddle-implied move versus
  history; bid/ask spreads and open interest; which monthly expiry and strike gives
  the best delta per dollar for a three-month view, and what IV crush after earnings
  would do to a call bought before it.

## 3. Synthesise

Write `packs/SYMBOL/report.md` yourself (do not delegate the verdict) with exactly
these sections:

1. **Stance** — bullish, neutral or bearish, plus confidence (low/medium/high).
2. **Thesis** — two to four sentences.
3. **Entry windows** — one to three, ordered by priority. Each has: name, start and
   end date, trigger, expiry (YYYY-MM-DD), strike, rationale, invalidation. A window
   that straddles earnings must say explicitly whether to be positioned before or
   after the report and why.
4. **Avoid** — dates or setups not to trade (e.g. buying calls into earnings at
   elevated IV).
5. **Technicals / Catalysts / Options market** — the condensed analyst findings.
6. **Risks** and **What would change this view**.

Rules: quote the pack's numbers; do not invent data; prefer monthly expiries with at
least three weeks of runway past the expected move; if the setup is poor say so with
an empty windows list. No generic disclaimers.

## 4. Report back

Reply to the user with the stance, the windows as a short table (window, dates,
trigger, expiry, strike), and the top two risks. Link the report path. If the user
passed `--expiry YYYY-MM`, make sure at least one window uses that monthly expiry or
explain why not.

## Alternative: one-shot model call

`go run ./cmd/mktpredict analyze SYMBOL` does steps 1 to 3 with a single Claude call
instead of subagents (uses `ANTHROPIC_API_KEY`, an `ant auth login` profile, or the
`claude` CLI's subscription login, whichever is present). Use it when the user asks
for the CLI result rather than the skill's multi-analyst pass.
