package analyst

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// DefaultAPIModel is used when no model is given on the command line.
const DefaultAPIModel = "claude-opus-5"

type apiAnalyst struct {
	client anthropic.Client
	model  string
}

func newAPIAnalyst(model string) *apiAnalyst {
	if model == "" {
		model = DefaultAPIModel
	}
	// The zero-arg client resolves ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN
	// or an `ant auth login` profile, in that order.
	return &apiAnalyst{client: anthropic.NewClient(option.WithMaxRetries(3)), model: model}
}

func (a *apiAnalyst) Name() string { return BackendAPI }

func (a *apiAnalyst) Analyze(ctx context.Context, packMarkdown string) (*Report, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: 16000,
		System: []anthropic.TextBlockParam{{
			Text: SystemPrompt + "\n\nRespond with a single JSON object, and nothing else, that validates against this JSON schema:\n" + Schema,
		}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(UserPrompt(packMarkdown))),
		},
	}
	// Streaming keeps long analyses clear of HTTP timeouts; the message is
	// accumulated and inspected once complete.
	stream := a.client.Messages.NewStreaming(ctx, params)
	msg := anthropic.Message{}
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			return nil, fmt.Errorf("analyst: accumulate stream: %w", err)
		}
	}
	if err := stream.Err(); err != nil {
		var apierr *anthropic.Error
		if errors.As(err, &apierr) {
			switch apierr.StatusCode {
			case 401, 403:
				return nil, fmt.Errorf("analyst: authentication failed (%d): check ANTHROPIC_API_KEY or run `ant auth login`; a Claude subscription needs the claude-code backend instead", apierr.StatusCode)
			case 404:
				return nil, fmt.Errorf("analyst: model %q not found", a.model)
			}
		}
		return nil, fmt.Errorf("analyst: claude api: %w", err)
	}
	if msg.StopReason == anthropic.StopReasonRefusal {
		return nil, fmt.Errorf("analyst: the model declined the request (%s)", msg.StopDetails.Explanation)
	}
	var text strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	r, err := parseReport(text.String())
	if err != nil {
		return nil, err
	}
	r.Backend, r.Model = BackendAPI, a.model
	return r, nil
}
