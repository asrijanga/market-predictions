package analyst

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// DefaultClaudeCodeModel is the model alias passed to the claude CLI when
// none is given; the CLI resolves it to the current Opus.
const DefaultClaudeCodeModel = "opus"

type claudeCodeAnalyst struct {
	binary string
	model  string
}

func newClaudeCodeAnalyst(model string) *claudeCodeAnalyst {
	if model == "" {
		model = DefaultClaudeCodeModel
	}
	return &claudeCodeAnalyst{binary: "claude", model: model}
}

func (c *claudeCodeAnalyst) Name() string { return BackendClaudeCode }

// Analyze runs `claude -p` in headless mode. The pack goes in on stdin so
// it never hits argument-length limits, tools are disabled so the run is
// a single model call, and --json-schema makes the CLI validate the output.
func (c *claudeCodeAnalyst) Analyze(ctx context.Context, packMarkdown string) (*Report, error) {
	args := []string{
		"-p",
		"--output-format", "json",
		"--json-schema", Schema,
		"--system-prompt", SystemPrompt,
		"--tools", "",
		"--no-session-persistence",
		"--model", c.model,
	}
	cmd := exec.CommandContext(ctx, c.binary, args...)
	cmd.Stdin = strings.NewReader(UserPrompt(packMarkdown))
	// Running inside another Claude Code session sets CLAUDECODE, which the
	// CLI treats as a nested invocation; clear it for the child.
	cmd.Env = append(os.Environ(), "CLAUDECODE=")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("analyst: claude cli: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var envelope struct {
		IsError          bool            `json:"is_error"`
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
	}
	out := stdout.Bytes()
	if i := bytes.IndexByte(out, '{'); i > 0 {
		out = out[i:] // skip any warning lines printed before the JSON
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		return nil, fmt.Errorf("analyst: decode claude cli output: %w: %.300s", err, stdout.String())
	}
	if envelope.IsError {
		return nil, fmt.Errorf("analyst: claude cli: %s", envelope.Result)
	}
	var r *Report
	if len(envelope.StructuredOutput) > 0 && string(envelope.StructuredOutput) != "null" {
		r = &Report{}
		if err := json.Unmarshal(envelope.StructuredOutput, r); err != nil {
			return nil, fmt.Errorf("analyst: decode structured output: %w", err)
		}
	} else {
		var err error
		if r, err = parseReport(envelope.Result); err != nil {
			return nil, err
		}
	}
	r.Backend, r.Model = BackendClaudeCode, c.model
	return r, nil
}
