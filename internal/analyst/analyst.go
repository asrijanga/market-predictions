// Package analyst turns a data pack into a call-timing report using Claude.
//
// Two backends are provided. The API backend talks to the Claude API with
// the official SDK and needs an API key (or an `ant auth login` profile).
// The Claude Code backend shells out to the `claude` CLI in headless mode,
// which authenticates with a Claude subscription. Detect picks whichever
// credentials are present.
package analyst

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Window is one recommended entry opportunity.
type Window struct {
	Name         string  `json:"name"`
	Start        string  `json:"start"`
	End          string  `json:"end"`
	Trigger      string  `json:"trigger"`
	Expiry       string  `json:"expiry"`
	Strike       float64 `json:"strike"`
	Rationale    string  `json:"rationale"`
	Invalidation string  `json:"invalidation"`
	Priority     int     `json:"priority"`
}

// Report is the structured verdict.
type Report struct {
	Symbol      string   `json:"symbol"`
	Stance      string   `json:"stance"` // bullish | neutral | bearish
	Thesis      string   `json:"thesis"`
	Technicals  string   `json:"technicals"`
	Catalysts   string   `json:"catalysts"`
	OptionsView string   `json:"options_view"`
	Windows     []Window `json:"windows"`
	Avoid       []string `json:"avoid"`
	Risks       []string `json:"risks"`
	ChangeMind  []string `json:"what_would_change_my_mind"`
	Confidence  string   `json:"confidence"` // low | medium | high

	Backend string `json:"backend,omitempty"`
	Model   string `json:"model,omitempty"`
}

// Analyst produces a Report from a rendered data pack.
type Analyst interface {
	Name() string
	Analyze(ctx context.Context, packMarkdown string) (*Report, error)
}

// Backend identifiers accepted on the command line.
const (
	BackendAuto       = "auto"
	BackendAPI        = "api"
	BackendClaudeCode = "claude-code"
)

// ErrNoCredentials is returned when neither backend can authenticate.
var ErrNoCredentials = errors.New(`no Claude credentials found: set ANTHROPIC_API_KEY (Claude API), run "ant auth login", or install Claude Code and run "claude" once to log in with your subscription`)

// New returns the analyst for backend, resolving "auto" via Detect.
func New(backend, model string) (Analyst, error) {
	if backend == BackendAuto {
		var err error
		if backend, err = Detect(); err != nil {
			return nil, err
		}
	}
	switch backend {
	case BackendAPI:
		return newAPIAnalyst(model), nil
	case BackendClaudeCode:
		return newClaudeCodeAnalyst(model), nil
	default:
		return nil, fmt.Errorf("unknown backend %q (want %s, %s or %s)", backend, BackendAuto, BackendAPI, BackendClaudeCode)
	}
}

// Detect chooses a backend from the environment: API credentials win,
// then a Claude Code install (subscription login).
func Detect() (string, error) {
	if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" || hasAntProfile() {
		return BackendAPI, nil
	}
	if _, err := exec.LookPath("claude"); err == nil {
		return BackendClaudeCode, nil
	}
	return "", ErrNoCredentials
}

func hasAntProfile() bool {
	dir := os.Getenv("ANTHROPIC_CONFIG_DIR")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return false
		}
		dir = filepath.Join(base, "anthropic")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "credentials", "*.json"))
	return len(matches) > 0
}

// SystemPrompt frames the task; identical for both backends so results are
// comparable across them.
const SystemPrompt = `You are a disciplined options analyst advising a retail trader who wants to buy call options on a single US stock within the next three months. You are given a data pack: a year of prices, trend statistics, the live option chain with implied volatility, upcoming events, SEC filings and recent headlines.

Your job is timing, not just direction: identify one to three concrete entry windows (date ranges) in the next three months, each with a trigger condition the trader can verify, the expiry and strike to buy, why, and what invalidates it. Think about: earnings-driven implied-volatility inflation and post-earnings IV crush; where the stock sits versus its moving averages and 52-week range; whether the trend is intact or extended; what the option market is pricing (straddle-implied move versus realised volatility); catalysts in the headlines and filings; bid/ask spreads and open interest for liquidity.

Be specific and quantitative. Use the numbers in the pack. Prefer monthly expiries with at least three weeks of runway past the expected move. If the setup is poor, say so: a "neutral" or "bearish" stance with an empty windows list and a clear avoid list is a valid answer. Never invent data that is not in the pack. This is analysis for an informed adult; do not pad with generic disclaimers.`

// Schema is the JSON schema both backends enforce on the model output.
const Schema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "symbol": {"type": "string"},
    "stance": {"type": "string", "enum": ["bullish", "neutral", "bearish"]},
    "thesis": {"type": "string", "description": "Two to four sentences: the core view and why now."},
    "technicals": {"type": "string", "description": "Trend, levels, momentum, volatility regime, with numbers."},
    "catalysts": {"type": "string", "description": "What the news and filings say; what is scheduled; what is priced in."},
    "options_view": {"type": "string", "description": "IV versus realised vol, term structure across expiries, skew, liquidity, which expiries are cheap or rich."},
    "windows": {
      "type": "array",
      "maxItems": 3,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "name": {"type": "string"},
          "start": {"type": "string", "description": "YYYY-MM-DD"},
          "end": {"type": "string", "description": "YYYY-MM-DD"},
          "trigger": {"type": "string", "description": "Verifiable condition to enter, e.g. close above X, IV below Y, the session after earnings."},
          "expiry": {"type": "string", "description": "YYYY-MM-DD of the option to buy"},
          "strike": {"type": "number"},
          "rationale": {"type": "string"},
          "invalidation": {"type": "string", "description": "What kills the trade or the window."},
          "priority": {"type": "integer", "minimum": 1, "maximum": 3}
        },
        "required": ["name", "start", "end", "trigger", "expiry", "strike", "rationale", "invalidation", "priority"]
      }
    },
    "avoid": {"type": "array", "items": {"type": "string"}, "description": "Dates or setups to avoid, e.g. buying calls into earnings at elevated IV."},
    "risks": {"type": "array", "items": {"type": "string"}},
    "what_would_change_my_mind": {"type": "array", "items": {"type": "string"}},
    "confidence": {"type": "string", "enum": ["low", "medium", "high"]}
  },
  "required": ["symbol", "stance", "thesis", "technicals", "catalysts", "options_view", "windows", "avoid", "risks", "what_would_change_my_mind", "confidence"]
}`

// UserPrompt wraps the pack for the model.
func UserPrompt(packMarkdown string) string {
	return "Analyse the following data pack and respond with a JSON object matching the required schema.\n\n" + packMarkdown
}

// parseReport extracts the JSON object from model text, tolerating a
// fenced code block or prose around it.
func parseReport(text string) (*Report, error) {
	text = strings.TrimSpace(text)
	if i := strings.Index(text, "```"); i >= 0 {
		rest := text[i+3:]
		rest = strings.TrimPrefix(rest, "json")
		if j := strings.Index(rest, "```"); j >= 0 {
			text = rest[:j]
		}
	}
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("analyst: no JSON object in response: %.200s", text)
	}
	var r Report
	if err := json.Unmarshal([]byte(text[start:end+1]), &r); err != nil {
		return nil, fmt.Errorf("analyst: decode report: %w", err)
	}
	return &r, nil
}

// RenderMarkdown formats a report for humans.
func RenderMarkdown(r *Report, asOf time.Time) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w("# %s call-timing report", r.Symbol)
	w("")
	w("As of %s. Stance: **%s**. Confidence: %s. Backend: %s (%s).", asOf.Format("2006-01-02"), r.Stance, r.Confidence, r.Backend, r.Model)
	w("")
	w("## Thesis")
	w("")
	w("%s", r.Thesis)
	w("")
	w("## Entry windows")
	w("")
	if len(r.Windows) == 0 {
		w("No entry recommended in the next three months.")
	}
	for _, win := range r.Windows {
		w("### %d. %s (%s to %s)", win.Priority, win.Name, win.Start, win.End)
		w("")
		w("- **Buy:** %s %g call", win.Expiry, win.Strike)
		w("- **Trigger:** %s", win.Trigger)
		w("- **Why:** %s", win.Rationale)
		w("- **Invalidation:** %s", win.Invalidation)
		w("")
	}
	section := func(title, body string) {
		w("## %s", title)
		w("")
		w("%s", body)
		w("")
	}
	section("Technicals", r.Technicals)
	section("Catalysts", r.Catalysts)
	section("Options market", r.OptionsView)
	list := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		w("## %s", title)
		w("")
		for _, it := range items {
			w("- %s", it)
		}
		w("")
	}
	list("Avoid", r.Avoid)
	list("Risks", r.Risks)
	list("What would change this view", r.ChangeMind)
	return b.String()
}
