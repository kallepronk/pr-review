// Package review runs the ticket pipeline: render prompts, fan out to the
// model, verify each finding adversarially, and turn survivors into GitHub
// review comments. It owns no I/O beyond the model; posting is main's job.
package review

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"text/template"

	"prreview/internal/gather"
	"prreview/internal/llm"
	"prreview/prompts"
)

type Finding struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	EndLine   int    `json:"end_line"`
	Severity  string `json:"severity"`
	Claim     string `json:"claim"`
	Evidence  string `json:"evidence"`
	Fix       string `json:"fix"`
	Dimension string `json:"dimension"`
	Score     int    `json:"score,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Ticket describes one question type. Tools=true routes through pi so the
// model can grep/read; false goes over plain HTTP with only the given text.
type Ticket struct {
	Name   string
	Prompt string
	Tools  bool
}

var HunkTickets = []Ticket{
	{Name: "bugs", Prompt: "ticket-bugs.md", Tools: true},
	{Name: "security", Prompt: "ticket-security.md", Tools: true},
	{Name: "claude-md", Prompt: "ticket-claude-md.md", Tools: false},
	{Name: "tests", Prompt: "ticket-tests.md", Tools: false},
}

type Config struct {
	Model       string // cheap model for text-only tickets and verification
	PiModel     string // same model in pi's naming, for tool tickets
	StrongModel string // architecture ticket; unused until structure.json exists
	Parallel    int
	MinScore    int
	MaxFindings int
	HTTP        llm.Asker
	Pi          llm.Asker
	RepoDir     string // checkout for pi tools; "" disables tool tickets
	Log         func(kind, name, prompt, answer string)
}

type Inputs struct {
	Hunks    []gather.Hunk
	ClaudeMD string
	Files    []string
}

// Run executes pass 1 (tickets) and pass 2 (verify) and returns findings that
// survived, sorted by score then severity, capped at MaxFindings.
func Run(ctx context.Context, cfg Config, in Inputs) ([]Finding, error) {
	tmpl, err := template.ParseFS(prompts.FS, "*.md")
	if err != nil {
		return nil, err
	}
	preamble, _ := prompts.FS.ReadFile("00-context.md")

	type job struct {
		t Ticket
		h gather.Hunk
	}
	var jobs []job
	for _, h := range in.Hunks {
		for _, t := range HunkTickets {
			if t.Tools && (cfg.RepoDir == "" || cfg.Pi == nil) {
				continue // ponytail: no checkout, skip tool tickets rather than fake them
			}
			jobs = append(jobs, job{t, h})
		}
	}

	var mu sync.Mutex
	var all []Finding
	var errs []error
	run(ctx, cfg.Parallel, len(jobs), func(i int) {
		j := jobs[i]
		var body strings.Builder
		data := map[string]any{"Path": j.h.Path, "Patch": j.h.Patch, "ClaudeMD": in.ClaudeMD, "Files": strings.Join(in.Files, "\n")}
		if err := tmpl.ExecuteTemplate(&body, j.t.Prompt, data); err != nil {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
			return
		}
		prompt := string(preamble) + "\n\n" + body.String()
		asker, model := cfg.HTTP, cfg.Model
		if j.t.Tools {
			asker, model = cfg.Pi, cfg.PiModel
		}
		raw, err := asker.Ask(ctx, model, prompt, cfg.RepoDir)
		if cfg.Log != nil {
			cfg.Log("ticket", j.t.Name+":"+j.h.Path, prompt, raw)
		}
		if err != nil {
			mu.Lock()
			errs = append(errs, fmt.Errorf("%s %s: %w", j.t.Name, j.h.Path, err))
			mu.Unlock()
			return
		}
		var out struct {
			Findings []Finding `json:"findings"`
		}
		if err := llm.ParseJSON(raw, &out); err != nil {
			mu.Lock()
			errs = append(errs, fmt.Errorf("%s %s: %w", j.t.Name, j.h.Path, err))
			mu.Unlock()
			return
		}
		for _, f := range out.Findings {
			f.Dimension = j.t.Name
			if f.Path == "" {
				f.Path = j.h.Path
			}
			if f.Path != j.h.Path {
				continue // model wandered; GitHub would reject the comment anyway
			}
			f.Line = j.h.Nearest(f.Line)
			if f.Line == 0 {
				continue
			}
			if f.EndLine < f.Line {
				f.EndLine = f.Line
			}
			mu.Lock()
			all = append(all, f)
			mu.Unlock()
		}
	})
	for _, e := range errs {
		log.Printf("warn: %v", e)
	}
	if len(jobs) > 0 && len(errs) == len(jobs) {
		return nil, fmt.Errorf("all %d tickets failed; not posting a review (first error: %v)", len(jobs), errs[0])
	}
	if len(all) == 0 {
		return nil, nil
	}

	all = dedupe(all)
	verified := make([]Finding, len(all))
	run(ctx, cfg.Parallel, len(all), func(i int) {
		f := all[i]
		h := hunkContaining(in.Hunks, f)
		fj, _ := json.MarshalIndent(f, "", "  ")
		var body strings.Builder
		_ = tmpl.ExecuteTemplate(&body, "rubric-verify.md", map[string]any{"Finding": string(fj), "Path": f.Path, "Patch": h.Patch})
		raw, err := cfg.HTTP.Ask(ctx, cfg.Model, body.String(), "")
		if cfg.Log != nil {
			cfg.Log("verify", f.Dimension+":"+f.Path, body.String(), raw)
		}
		var v struct {
			Score  int    `json:"score"`
			Reason string `json:"reason"`
		}
		if err == nil {
			err = llm.ParseJSON(raw, &v)
		}
		if err != nil {
			log.Printf("warn: verify %s %s: %v", f.Dimension, f.Path, err)
			v.Score = 0
		}
		f.Score, f.Reason = v.Score, v.Reason
		verified[i] = f
	})

	var kept []Finding
	for _, f := range verified {
		if f.Score >= cfg.MinScore {
			kept = append(kept, f)
		}
	}
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].Score != kept[j].Score {
			return kept[i].Score > kept[j].Score
		}
		return sevRank(kept[i].Severity) > sevRank(kept[j].Severity)
	})
	if cfg.MaxFindings > 0 && len(kept) > cfg.MaxFindings {
		kept = kept[:cfg.MaxFindings]
	}
	return kept, nil
}

func run(ctx context.Context, parallel, n int, fn func(i int)) {
	if parallel < 1 {
		parallel = 1
	}
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}

func dedupe(fs []Finding) []Finding {
	seen := map[string]bool{}
	var out []Finding
	for _, f := range fs {
		k := fmt.Sprintf("%s:%d:%s", f.Path, f.Line/3, f.Dimension) // ponytail: 3-line buckets
		if !seen[k] {
			seen[k] = true
			out = append(out, f)
		}
	}
	return out
}

func hunkContaining(hunks []gather.Hunk, f Finding) gather.Hunk {
	for _, h := range hunks {
		if h.Path != f.Path {
			continue
		}
		for _, l := range h.ChangedLines {
			if l == f.Line {
				return h
			}
		}
	}
	for _, h := range hunks {
		if h.Path == f.Path {
			return h
		}
	}
	return gather.Hunk{}
}

func sevRank(s string) int {
	switch s {
	case "high":
		return 3
	case "medium":
		return 2
	default:
		return 1
	}
}

// CommentBody renders one inline comment. Template first; no model call.
func CommentBody(f Finding, owner, repo, sha string) string {
	link := fmt.Sprintf("https://github.com/%s/%s/blob/%s/%s#L%d-L%d", owner, repo, sha, f.Path, max(1, f.Line-1), f.EndLine+1)
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** (%s)\n\n%s\n\n", f.Claim, f.Dimension, f.Fix)
	if f.Evidence != "" {
		fmt.Fprintf(&b, "```\n%s\n```\n\n", strings.TrimSpace(f.Evidence))
	}
	fmt.Fprintf(&b, "%s", link)
	return b.String()
}

// Summary is the review's top-level body.
func Summary(n, round int) string {
	if n == 0 {
		return fmt.Sprintf("### Code review (round %d)\n\nNo issues found. Checked for bugs, security, project-instruction compliance and test coverage on changed lines.\n\n<sub>React 👍 if useful, 👎 if noise.</sub>", round)
	}
	return fmt.Sprintf("### Code review (round %d)\n\nFound %d issue(s), see inline comments. Each was checked twice; anything below the confidence bar was dropped.\n\n<sub>React 👍 if useful, 👎 if noise.</sub>", round, n)
}
