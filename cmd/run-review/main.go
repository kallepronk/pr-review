// run-review reviews one pull request, one round at a time.
//
//	run-review -pr owner/repo#123 [-workdir /work] [-repo /work/repo] [-dry-run]
//
// Round 1 reviews the whole diff. Later rounds review only what changed since
// the last reviewed SHA (state.json). Reconciling earlier findings against
// author replies is not implemented yet; see PLAN.md section 5.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"prreview/internal/gather"
	"prreview/internal/github"
	"prreview/internal/llm"
	"prreview/internal/review"
)

func main() {
	var (
		prRef       = flag.String("pr", "", "owner/repo#N or PR URL (required)")
		workdir     = flag.String("workdir", ".", "where state.json and rounds/ live")
		repoDir     = flag.String("repo", "", "local checkout for read-only tools; empty disables tool tickets")
		provider    = flag.String("provider", llm.Provider(), "anthropic, gemini or openrouter (text-only tickets; pi follows)")
		model       = flag.String("model", envOr("PRREVIEW_MODEL", ""), "cheap model id in the provider's naming; default per provider")
		strong      = flag.String("strong-model", envOr("PRREVIEW_STRONG_MODEL", ""), "model for the architecture ticket (unused yet)")
		parallel    = flag.Int("parallel", 6, "concurrent model calls")
		minScore    = flag.Int("min-score", 80, "verification score needed to post")
		maxFindings = flag.Int("max-findings", 10, "cap on posted findings per round")
		maxHunks    = flag.Int("max-hunks", 60, "skip review above this many hunks")
		dryRun      = flag.Bool("dry-run", false, "print the review instead of posting")
	)
	flag.Parse()

	owner, repo, num, err := parsePR(*prRef)
	if err != nil {
		log.Fatal(err)
	}
	if *model == "" {
		*model = llm.DefaultModel(*provider)
	}
	var text llm.Asker = llm.NewHTTP(*provider)
	if *provider == "anthropic" {
		text = llm.NewAnthropic()
	}
	piProvider, piModel := llm.PiModel(*provider, *model)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	gh := github.New()
	pr, err := gh.GetPR(owner, repo, num)
	if err != nil {
		log.Fatal(err)
	}
	state, err := gather.LoadState(*workdir)
	if err != nil {
		log.Fatal(err)
	}
	if reason := ineligible(pr, state); reason != "" {
		log.Printf("skip: %s", reason)
		return
	}
	round := state.Round + 1

	// Round 1: whole diff. Later: only what moved since the last reviewed SHA.
	var diff string
	if state.LastSHA == "" {
		diff, err = gh.Diff(owner, repo, num)
	} else {
		diff, err = gh.Compare(owner, repo, state.LastSHA, pr.Head.SHA)
	}
	if err != nil {
		log.Fatal(err)
	}
	hunks := gather.SplitDiff(diff)
	if len(hunks) > *maxHunks {
		msg := fmt.Sprintf("Skipping automated review: %d hunks exceeds the cap of %d. Review manually.", len(hunks), *maxHunks)
		log.Print(msg)
		if !*dryRun {
			_ = gh.IssueComment(owner, repo, num, msg)
		}
		return
	}
	files := gather.Paths(hunks)

	claudeMD := instructions(gh, owner, repo, pr.Head.SHA, files)

	roundDir := filepath.Join(*workdir, "rounds", strconv.Itoa(round))
	_ = os.MkdirAll(roundDir, 0o755)
	_ = os.WriteFile(filepath.Join(roundDir, "diff.patch"), []byte(diff), 0o644)

	cfg := review.Config{
		Model: *model, PiModel: piModel, StrongModel: *strong, Parallel: *parallel, MinScore: *minScore, MaxFindings: *maxFindings,
		HTTP: text, Pi: llm.NewPi(piProvider), RepoDir: *repoDir,
		Log: logger(roundDir),
	}
	findings, err := review.Run(ctx, cfg, review.Inputs{Hunks: hunks, ClaudeMD: claudeMD, Files: files})
	if err != nil {
		log.Fatal(err)
	}

	var comments []github.ReviewComment
	for _, f := range findings {
		if state.AlreadyPosted(f.Path, f.Line, f.Dimension) {
			continue
		}
		comments = append(comments, github.ReviewComment{Path: f.Path, Line: f.Line, Side: "RIGHT", Body: review.CommentBody(f, owner, repo, pr.Head.SHA)})
		state.Findings = append(state.Findings, gather.PostedFinding{Path: f.Path, Line: f.Line, Dimension: f.Dimension, Claim: f.Claim, Round: round})
	}
	body := review.Summary(len(comments), round)

	if *dryRun {
		fmt.Println(body)
		for _, c := range comments {
			fmt.Printf("\n--- %s:%d\n%s\n", c.Path, c.Line, c.Body)
		}
		return
	}

	// Re-check right before posting: the PR may have closed mid-run.
	if pr2, err := gh.GetPR(owner, repo, num); err != nil || pr2.State != "open" {
		log.Printf("skip post: PR no longer open (%v)", err)
		return
	}
	if err := gh.CreateReview(owner, repo, num, pr.Head.SHA, body, comments); err != nil {
		log.Fatal(err)
	}
	state.Round = round
	state.LastSHA = pr.Head.SHA
	if err := state.Save(*workdir); err != nil {
		log.Fatal(err)
	}
	log.Printf("posted round %d with %d comment(s)", round, len(comments))
}

func ineligible(pr *github.PR, s *gather.State) string {
	switch {
	case pr.State != "open":
		return "PR is " + pr.State
	case pr.Draft:
		return "PR is a draft"
	case pr.User.Type == "Bot" || strings.HasSuffix(pr.User.Login, "[bot]"):
		return "PR author is a bot"
	case s.LastSHA == pr.Head.SHA:
		return "head SHA already reviewed"
	}
	return ""
}

// instructions concatenates CLAUDE.md / AGENTS.md from the root and every changed directory.
func instructions(gh *github.Client, owner, repo, ref string, files []string) string {
	dirs := map[string]bool{"": true}
	for _, f := range files {
		for d := filepath.Dir(f); d != "." && d != "/"; d = filepath.Dir(d) {
			dirs[d+"/"] = true
		}
	}
	var b strings.Builder
	for d := range dirs {
		for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
			content, err := gh.GetFile(owner, repo, d+name, ref)
			if err != nil {
				log.Printf("warn: %v", err)
				continue
			}
			if content != "" {
				fmt.Fprintf(&b, "# %s%s\n\n%s\n\n", d, name, content)
			}
		}
	}
	if b.Len() == 0 {
		return "(no project instruction files found)"
	}
	return b.String()
}

func logger(dir string) func(kind, name, prompt, answer string) {
	var n int
	return func(kind, name, prompt, answer string) {
		n++
		safe := regexp.MustCompile(`[^a-zA-Z0-9._-]+`).ReplaceAllString(name, "_")
		base := filepath.Join(dir, fmt.Sprintf("%03d-%s-%s", n, kind, safe))
		_ = os.WriteFile(base+".prompt.md", []byte(prompt), 0o644)
		_ = os.WriteFile(base+".answer.txt", []byte(answer), 0o644)
	}
}

var prRe = regexp.MustCompile(`^(?:https://github\.com/)?([^/#\s]+)/([^/#\s]+?)(?:/pull/|#)(\d+)$`)

func parsePR(s string) (owner, repo string, n int, err error) {
	m := prRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", "", 0, fmt.Errorf("-pr must be owner/repo#N or a PR URL, got %q", s)
	}
	n, _ = strconv.Atoi(m[3])
	return m[1], m[2], n, nil
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
