package llm

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// Anthropic asks Claude directly through the official SDK. Reads
// ANTHROPIC_API_KEY (or ANTHROPIC_BASE_URL for a gateway) from the environment.
type Anthropic struct {
	client anthropic.Client
}

func NewAnthropic() *Anthropic {
	return &Anthropic{client: anthropic.NewClient()}
}

// maxTokens bounds one ticket's JSON answer; findings are short by design.
const maxTokens = 4096

func (a *Anthropic) Ask(ctx context.Context, model, prompt, _ string) (string, error) {
	resp, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:       model,
		MaxTokens:   maxTokens,
		Temperature: anthropic.Float(0),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return "", err
	}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return "", errors.New("anthropic: request refused")
	}
	var b strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String(), nil
}

// Provider picks the text-only runner. PRREVIEW_PROVIDER=anthropic|openrouter;
// unset means anthropic when ANTHROPIC_API_KEY is present, else openrouter.
func Provider() string {
	if p := os.Getenv("PRREVIEW_PROVIDER"); p != "" {
		return p
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return "anthropic"
	}
	return "openrouter"
}

// DefaultModel is the cheap model per provider.
func DefaultModel(provider string) string {
	if provider == "anthropic" {
		return "claude-haiku-4-5"
	}
	return "anthropic/claude-haiku-4.5"
}

// PiModel maps a provider model id to pi's --provider/--model pair.
func PiModel(provider, model string) (piProvider, piModel string) {
	if provider == "anthropic" {
		return "anthropic", model
	}
	return "openrouter", model
}
