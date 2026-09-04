// Package llm has two ways to ask a model one question: plain HTTP to an
// OpenAI-compatible chat endpoint (OpenRouter directly, or the Sprites
// connector gateway), and the pi coding agent for tickets that need read-only
// tools. Both return the model's raw text; callers parse JSON out of it.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Asker answers one prompt. cwd is the repo checkout, used only by tool-capable runners.
type Asker interface {
	Ask(ctx context.Context, model, prompt, cwd string) (string, error)
}

// HTTP talks to /chat/completions. PRREVIEW_LLM_BASE defaults to OpenRouter;
// OPENROUTER_API_KEY is optional (empty when behind the Sprites gateway).
type HTTP struct {
	Base string
	Key  string
	http *http.Client
}

func NewHTTP() *HTTP {
	base := os.Getenv("PRREVIEW_LLM_BASE")
	if base == "" {
		base = "https://openrouter.ai/api/v1"
	}
	return &HTTP{Base: strings.TrimRight(base, "/"), Key: os.Getenv("OPENROUTER_API_KEY"), http: &http.Client{Timeout: 3 * time.Minute}}
}

func (h *HTTP) Ask(ctx context.Context, model, prompt, _ string) (string, error) {
	in := map[string]any{
		"model":           model,
		"messages":        []map[string]string{{"role": "user", "content": prompt}},
		"temperature":     0,
		"response_format": map[string]string{"type": "json_object"},
	}
	b, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", h.Base+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.Key != "" {
		req.Header.Set("Authorization", "Bearer "+h.Key)
	}
	resp, err := h.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("llm %s: %d %s", model, resp.StatusCode, string(raw))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", errors.New("llm: no choices in response")
	}
	return out.Choices[0].Message.Content, nil
}

// Pi shells out to `pi -p` with a read-only tool set. Keys are pi's business:
// it reads ANTHROPIC_API_KEY / OPENROUTER_API_KEY from the environment.
type Pi struct {
	Bin      string
	Provider string // pi --provider value (anthropic, openrouter, ...)
}

func NewPi(provider string) *Pi {
	bin := os.Getenv("PRREVIEW_PI_BIN")
	if bin == "" {
		bin = "pi"
	}
	return &Pi{Bin: bin, Provider: provider}
}

func (p *Pi) Ask(ctx context.Context, model, prompt, cwd string) (string, error) {
	args := []string{"-p", "--no-session", "--no-context-files", "--tools", "read,grep,find,ls", "--provider", p.Provider, "--model", model, prompt}
	cmd := exec.CommandContext(ctx, p.Bin, args...)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pi: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// ParseJSON pulls the first JSON object out of model output, tolerating fences
// and chatter around it, and unmarshals it into out.
func ParseJSON(raw string, out any) error {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "{"); i >= 0 {
		s = s[i:]
	}
	if j := strings.LastIndex(s, "}"); j >= 0 {
		s = s[:j+1]
	}
	if err := json.Unmarshal([]byte(s), out); err != nil {
		return fmt.Errorf("parse model json: %w; raw: %s", err, truncate(raw, 400))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
