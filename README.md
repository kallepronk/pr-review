# pr-review

Automated pull request review on Fly Sprites with cheap models and narrow, deterministic prompts. Design and rationale: [PLAN.md](PLAN.md).

## Layout

```
cmd/run-review/        entry point: gather → tickets → verify → post → state
internal/gather/       diff splitting, changed-line tracking, state.json
internal/review/       ticket fan-out, verification, comment rendering
internal/llm/          HTTP (OpenAI-compatible) and pi runners
internal/github/       minimal REST + GraphQL client, gateway-aware
prompts/               every model-facing prompt, embedded in the binary
scripts/bootstrap.sh   one-time sprite setup (pi, build binary from source, clone, checkpoint)
scripts/round.sh       one review round inside the sprite
.github/workflows/     pr-review.yml: trigger to copy into target repos
```

## Local run (phase 1)

Needs Go, a model key (Gemini, Anthropic or OpenRouter), a GitHub token with `repo` scope, and optionally `pi` (`npm i -g @earendil-works/pi-coding-agent`) plus a local checkout of the target repo for the tool-using tickets.

```bash
export GEMINI_API_KEY=...          # or ANTHROPIC_API_KEY / OPENROUTER_API_KEY
export GH_TOKEN=$(gh auth token)
go run ./cmd/run-review -pr owner/repo#123 -repo /path/to/checkout -dry-run
```

Provider is picked from whichever key is set: Anthropic (`claude-haiku-4-5`), then Gemini (`gemini-2.5-flash-lite`, via Google's OpenAI-compatible endpoint), else OpenRouter. Override with `-provider` / `-model`.

Without `-repo`, only the text-only tickets run (project instructions, tests). Drop `-dry-run` to post. Every prompt and raw answer lands in `rounds/N/` for inspection.

```bash
go test ./...
```

## Configuration

| Env / flag | Purpose |
|---|---|
| `PRREVIEW_PROVIDER` / `-provider` | `anthropic`, `gemini` or `openrouter`; default from which key is present |
| `PRREVIEW_MODEL` / `-model` | cheap model id in the provider's naming; default per provider |
| `ANTHROPIC_API_KEY`, `GEMINI_API_KEY` | Anthropic / Gemini providers, also read by pi |
| `OPENROUTER_API_KEY`, `PRREVIEW_LLM_BASE` | OpenRouter provider; base defaults to openrouter.ai, or the Sprites gateway |
| `GH_TOKEN`, `PRREVIEW_GITHUB_BASE` | GitHub auth; in Actions the job token is passed into the sprite |
| `-min-score` | verification threshold, default 80 |
| `-max-findings`, `-max-hunks` | caps per round |

## Not done yet

- Reconcile prior findings against author replies (round 2 currently reviews only the delta diff). Prompt exists: `prompts/ticket-reconcile.md`.
- Architecture ticket: needs `structure.json` extraction and the repo map. Prompt exists: `prompts/ticket-architecture.md`.
- Sprites connector gateway path (OpenRouter managed, GitHub OAuth) so no keys enter the sprite; today keys pass via `sprite exec --env`.
