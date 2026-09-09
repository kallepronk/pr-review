// run-review reviews one pull request, one round at a time.
//
//	run-review -pr owner/repo#123 [-workdir /work] [-repo /work/repo] [-full] [-force] [-dry-run]
//
// Each round: reconcile earlier findings against author replies and new
// commits, then review the diff since the last reviewed SHA (or the whole diff
// with -full), then post. -force reviews drafts and re-runs on an already
// reviewed SHA; the /review command sets it.
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
	"sync/atomic"
	"time"

	"prreview/internal/gather"
	"prreview/internal/github"
	"prreview/internal/llm"
	"prreview/internal/review"
)

func main() {
	var (
		prRef        = flag.String("pr", "", "owner/repo#N or PR URL (required)")
		workdir      = flag.String("workdir", ".", "where state.json and rounds/ live")
		repoDir      = flag.String("repo", "", "local checkout for read-only tools; empty disables tool tickets")
		provider     = flag.String("provider", llm.Provider(), "anthropic, gemini or openrouter (text-only tickets; pi follows)")
		model        = flag.String("model", envOr("PRREVIEW_MODEL", ""), "cheap model id in the provider's naming; default per provider")
		strong       = flag.String("strong-model", envOr("PRREVIEW_STRONG_MODEL", ""), "model for the architecture ticket (unused yet)")
		parallel     = flag.Int("parallel", 6, "concurrent model calls")
		minScore     = flag.Int("min-score", 80, "verification score needed to post")
		maxFindings  = flag.Int("max-findings", 10, "cap on posted findings per round")
		maxHunks     = flag.Int("max-hunks", 60, "skip review above this many hunks")
		maxRebuttals = flag.Int("max-rebuttals", 1, "replies on a disputed thread before going quiet")
		full         = flag.Bool("full", false, "review the whole diff, not just changes since the last round")
		force        = flag.Bool("force", false, "run on drafts and on an already reviewed SHA (slash command)")
		dryRun       = flag.Bool("dry-run", false, "print instead of posting")
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
	if reason := ineligible(pr, state, *force); reason != "" {
		log.Printf("skip: %s", reason)
		return
	}
	round := state.Round + 1
	head := pr.Head.SHA
	roundDir := filepath.Join(*workdir, "rounds", strconv.Itoa(round))
	_ = os.MkdirAll(roundDir, 0o755)

	cfg := review.Config{
		Model: *model, PiModel: piModel, StrongModel: *strong, Parallel: *parallel, MinScore: *minScore,
		MaxFindings: *maxFindings, MaxRebuttals: *maxRebuttals,
		HTTP: text, Pi: llm.NewPi(piProvider), RepoDir: *repoDir,
		Log: logger(roundDir),
	}

	// Diff to review: whole PR on round 1 or -full, else only what moved since the last round.
	var diff string
	if state.LastSHA == "" || *full {
		diff, err = gh.Diff(owner, repo, num)
	} else if state.LastSHA != head {
		diff, err = gh.Compare(owner, repo, state.LastSHA, head)
	}
	if err != nil {
		log.Fatal(err)
	}
	hunks, skipped := gather.FilterIgnored(gather.SplitDiff(diff))
	_ = os.WriteFile(filepath.Join(roundDir, "diff.patch"), []byte(diff), 0o644)

	// Phase 1: reconcile earlier findings. Needs the delta on their files, never the full diff.
	var actions []review.Action
	if len(state.Open()) > 0 {
		delta := hunks
		if *full && state.LastSHA != "" && state.LastSHA != head {
			d, err := gh.Compare(owner, repo, state.LastSHA, head)
			if err != nil {
				log.Fatal(err)
			}
			delta, _ = gather.FilterIgnored(gather.SplitDiff(d))
		} else if *full {
			delta = nil
		}
		threads, err := gh.ReviewThreads(owner, repo, num)
		if err != nil {
			log.Fatal(err)
		}
		actions, err = review.Reconcile(ctx, cfg, state, threads, delta)
		if err != nil {
			log.Fatal(err)
		}
	}

	// Phase 2: review new hunks.
	var findings []review.Finding
	if len(hunks) > *maxHunks {
		msg := fmt.Sprintf("Skipping automated review: %d hunks exceeds the cap of %d. Review manually or use smaller PRs.", len(hunks), *maxHunks)
		log.Print(msg)
		if !*dryRun {
			_ = gh.IssueComment(owner, repo, num, msg)
		}
		hunks = nil
	}
	if len(hunks) > 0 {
		files := gather.Paths(hunks)
		claudeMD := instructions(gh, owner, repo, head, files)
		findings, err = review.Run(ctx, cfg, review.Inputs{Hunks: hunks, ClaudeMD: claudeMD, Files: files})
		if err != nil {
			log.Fatal(err)
		}
	}

	comments := []github.ReviewComment{} // never nil: GitHub rejects "comments": null
	var newFindings []gather.PostedFinding
	for _, f := range findings {
		if state.AlreadyPosted(f.Path, f.Line, f.Dimension) {
			continue
		}
		comments = append(comments, github.ReviewComment{Path: f.Path, Line: f.Line, Side: "RIGHT", Body: review.CommentBody(f, owner, repo, head)})
		newFindings = append(newFindings, gather.PostedFinding{Path: f.Path, Line: f.Line, Dimension: f.Dimension, Claim: f.Claim, Round: round})
	}
	resolved, rebutted := 0, 0
	for _, a := range actions {
		if a.Resolve {
			resolved++
		} else {
			rebutted++
		}
	}
	body := review.Summary(round, len(comments), resolved, rebutted, skipped)

	if *dryRun {
		fmt.Println(body)
		for _, c := range comments {
			fmt.Printf("\n--- %s:%d\n%s\n", c.Path, c.Line, c.Body)
		}
		for _, a := range actions {
			f := state.Findings[a.Index]
			fmt.Printf("\n--- reconcile %s:%d -> %s (resolve=%v)\n%s\n", f.Path, f.Line, a.Verdict.Status, a.Resolve, a.Reply)
		}
		return
	}

	// Re-check right before posting: the PR may have closed mid-run.
	if pr2, err := gh.GetPR(owner, repo, num); err != nil || pr2.State != "open" {
		log.Printf("skip post: PR no longer open (%v)", err)
		return
	}

	// Apply reconcile actions thread by thread.
	for _, a := range actions {
		f := &state.Findings[a.Index]
		if a.Reply != "" {
			if err := gh.ReplyToReviewComment(owner, repo, num, f.CommentID, a.Reply); err != nil {
				log.Printf("warn: reply on %s: %v", f.Key(), err)
				continue
			}
		}
		if a.Resolve {
			if err := gh.ResolveThread(f.ThreadID); err != nil {
				log.Printf("warn: resolve %s: %v", f.Key(), err)
			} else {
				f.Resolved = true
			}
		} else {
			state.RebuttalCount[f.Key()]++
		}
	}

	// Post the review only when there is something to say; quiet pushes stay quiet.
	if len(comments) > 0 || len(actions) > 0 || round == 1 {
		reviewID, err := gh.CreateReview(owner, repo, num, head, body, comments)
		if err != nil {
			log.Fatal(err)
		}
		posted, err := gh.ReviewComments(owner, repo, num, reviewID)
		if err != nil {
			log.Printf("warn: could not list posted comments; threads will not reconcile: %v", err)
		}
		for i := range newFindings {
			newFindings[i].CommentID = commentIDFor(posted, newFindings[i])
		}
		state.Findings = append(state.Findings, newFindings...)
	}
	state.Round = round
	state.LastSHA = head
	if err := state.Save(*workdir); err != nil {
		log.Fatal(err)
	}
	log.Printf("round %d: %d new comment(s), %d resolved, %d rebutted, %d file(s) skipped", round, len(comments), resolved, rebutted, len(skipped))
}

func ineligible(pr *github.PR, s *gather.State, force bool) string {
	switch {
	case pr.State != "open":
		return "PR is " + pr.State
	case pr.User.Type == "Bot" || strings.HasSuffix(pr.User.Login, "[bot]"):
		return "PR author is a bot"
	case pr.Draft && !force:
		return "PR is a draft (use /review to review anyway)"
	case s.LastSHA == pr.Head.SHA && !force:
		return "head SHA already reviewed"
	}
	return ""
}

func commentIDFor(posted []github.PostedComment, f gather.PostedFinding) int64 {
	for _, p := range posted {
		line := p.Line
		if line == 0 {
			line = p.OriginalLine
		}
		if p.Path == f.Path && line == f.Line {
			return p.ID
		}
	}
	return 0
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
	var n atomic.Int32 // called from parallel ticket goroutines
	return func(kind, name, prompt, answer string) {
		seq := n.Add(1)
		safe := regexp.MustCompile(`[^a-zA-Z0-9._-]+`).ReplaceAllString(name, "_")
		base := filepath.Join(dir, fmt.Sprintf("%03d-%s-%s", seq, kind, safe))
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
