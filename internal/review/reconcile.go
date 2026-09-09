package review

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"text/template"

	"prreview/internal/gather"
	"prreview/internal/github"
	"prreview/internal/llm"
	"prreview/prompts"
)

// Verdict is one reconcile ticket's answer.
type Verdict struct {
	Status string `json:"status"` // fixed | disputed_valid | disputed_invalid | unchanged
	Reply  string `json:"reply"`
}

// Action is what the caller should do to the thread; posting is main's job.
type Action struct {
	Index   int // index into state.Findings
	Verdict Verdict
	Reply   string // final text to post, "" for none
	Resolve bool
}

// Reconcile decides, for each open prior finding, what happened to it: fixed by
// the new diff, disputed by a human, or untouched. Findings with no human
// reply and no change on their file are skipped without a model call.
func Reconcile(ctx context.Context, cfg Config, state *gather.State, threads []github.Thread, delta []gather.Hunk) ([]Action, error) {
	tmpl, err := template.ParseFS(prompts.FS, "ticket-reconcile.md")
	if err != nil {
		return nil, err
	}
	byComment := map[int64]github.Thread{}
	for _, t := range threads {
		if len(t.Comments) > 0 {
			byComment[t.Comments[0].DatabaseID] = t
		}
	}
	patchFor := map[string]string{}
	for _, h := range delta {
		patchFor[h.Path] += h.Patch
	}

	var actions []Action
	for _, i := range state.Open() {
		f := &state.Findings[i]
		t, ok := byComment[f.CommentID]
		if !ok {
			continue // thread deleted or not ours
		}
		f.ThreadID = t.ID
		if t.IsResolved {
			f.Resolved = true // a human resolved it; nothing to say
			continue
		}
		var replies []string
		for _, c := range t.Comments[1:] {
			if !isBot(c.Author) {
				replies = append(replies, c.Author+": "+c.Body)
			}
		}
		patch := patchFor[f.Path]
		if len(replies) == 0 && patch == "" {
			continue // ponytail: nothing new, no ticket
		}
		if len(replies) == 0 && cfg.MaxRebuttals > 0 && state.RebuttalCount[f.Key()] >= cfg.MaxRebuttals {
			continue
		}

		fj, _ := json.MarshalIndent(map[string]any{"path": f.Path, "line": f.Line, "dimension": f.Dimension, "claim": f.Claim}, "", "  ")
		var body strings.Builder
		if err := tmpl.Execute(&body, map[string]any{"Finding": string(fj), "Replies": strings.Join(replies, "\n\n"), "Path": f.Path, "Patch": patch}); err != nil {
			return nil, err
		}
		raw, err := cfg.HTTP.Ask(ctx, cfg.Model, body.String(), "")
		if cfg.Log != nil {
			cfg.Log("reconcile", f.Dimension+":"+f.Path, body.String(), raw)
		}
		var v Verdict
		if err == nil {
			err = llm.ParseJSON(raw, &v)
		}
		if err != nil {
			log.Printf("warn: reconcile %s: %v", f.Key(), err)
			continue
		}

		a := Action{Index: i, Verdict: v}
		switch v.Status {
		case "fixed":
			a.Reply = "Fixed, resolving."
			a.Resolve = true
		case "disputed_invalid":
			a.Reply = firstNonEmpty(v.Reply, "You're right, withdrawing this one.")
			a.Resolve = true
		case "disputed_valid":
			if state.RebuttalCount[f.Key()] >= cfg.MaxRebuttals {
				continue // said our piece once; humans decide
			}
			a.Reply = firstNonEmpty(v.Reply, "I still think this holds; see the original comment.")
		default:
			continue
		}
		actions = append(actions, a)
	}
	return actions, nil
}

func isBot(login string) bool {
	return strings.HasSuffix(login, "[bot]") || login == "github-actions"
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
