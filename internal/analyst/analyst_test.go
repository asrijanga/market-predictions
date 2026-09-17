package analyst

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseReportTolerantOfFences(t *testing.T) {
	text := "Here you go:\n```json\n{\"symbol\":\"AAPL\",\"stance\":\"bullish\",\"windows\":[{\"name\":\"post-earnings\",\"strike\":335}],\"confidence\":\"medium\"}\n```\nDone."
	r, err := parseReport(text)
	if err != nil {
		t.Fatal(err)
	}
	if r.Symbol != "AAPL" || len(r.Windows) != 1 || r.Windows[0].Strike != 335 {
		t.Fatalf("report = %+v", r)
	}
	if _, err := parseReport("no json here"); err == nil {
		t.Fatal("expected error")
	}
}

func TestClaudeCodeBackendParsesEnvelope(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	script := `#!/bin/sh
# Echo the prompt length to stderr so the test can see stdin was wired.
wc -c >&2
echo 'Warning: something harmless'
echo '{"is_error":false,"result":"ignored","structured_output":{"symbol":"NVDA","stance":"neutral","windows":[],"confidence":"low"}}'
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &claudeCodeAnalyst{binary: fake, model: "opus"}
	r, err := a.Analyze(context.Background(), "# pack")
	if err != nil {
		t.Fatal(err)
	}
	if r.Symbol != "NVDA" || r.Stance != "neutral" || r.Backend != BackendClaudeCode {
		t.Fatalf("report = %+v", r)
	}
}

func TestClaudeCodeBackendSurfacesErrors(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho '{\"is_error\":true,\"result\":\"Authentication error\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &claudeCodeAnalyst{binary: fake, model: "opus"}
	_, err := a.Analyze(context.Background(), "# pack")
	if err == nil || !strings.Contains(err.Error(), "Authentication error") {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderMarkdown(t *testing.T) {
	r := &Report{Symbol: "AAPL", Stance: "bullish", Confidence: "medium", Backend: "api", Model: "claude-opus-5",
		Thesis: "t", Windows: []Window{{Name: "w", Start: "2026-10-01", End: "2026-10-15", Expiry: "2026-12-18", Strike: 335, Priority: 1}},
		Risks: []string{"r1"}}
	md := RenderMarkdown(r, time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC))
	for _, want := range []string{"# AAPL call-timing report", "2026-12-18 335 call", "- r1", "### 1. w"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
}

func TestNewRejectsUnknownBackend(t *testing.T) {
	if _, err := New("nope", ""); err == nil {
		t.Fatal("expected error")
	}
}
