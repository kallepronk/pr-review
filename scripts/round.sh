#!/usr/bin/env bash
# One review round inside the sprite. Called by the workflow via sprite exec.
# Env: PR (owner/repo#N), HEAD_SHA, ANTHROPIC_API_KEY, GH_TOKEN, optional
# PRREVIEW_MODEL. Extra args are passed to run-review (e.g. -dry-run).
set -euo pipefail

cd /work/repo
git -c "http.extraheader=Authorization: Bearer ${GH_TOKEN}" fetch --quiet origin "$HEAD_SHA"
git checkout --quiet "$HEAD_SHA"

# Refresh the reviewer binary when its repo moved (cheap: shallow pull + build).
if git -C /work/pr-review pull --quiet --ff-only 2>/dev/null | grep -q .; then
  (cd /work/pr-review && go build -o /usr/local/bin/run-review ./cmd/run-review)
fi

exec flock -n /work/.lock run-review -pr "$PR" -workdir /work -repo /work/repo "$@"
