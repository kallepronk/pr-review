#!/usr/bin/env bash
# One review round inside the sprite. Called by the workflow via sprite exec.
# Env: PR (owner/repo#N), HEAD_SHA, ANTHROPIC_API_KEY, GH_TOKEN, optional
# PRREVIEW_MODEL. Extra args are passed to run-review (e.g. -dry-run).
set -euo pipefail

cd /work/repo
REPO_SLUG="${PR%%#*}"
# Diagnostics: never the token itself, only its shape and what the API says about it.
echo "GH_TOKEN: prefix=${GH_TOKEN:0:4} len=${#GH_TOKEN} api=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer ${GH_TOKEN}" "https://api.github.com/repos/${REPO_SLUG}")"
# Same header shape actions/checkout uses. Public repos also work anonymously,
# so fall back to that rather than failing the round on an auth hiccup.
AUTH="Authorization: basic $(printf 'x-access-token:%s' "$GH_TOKEN" | base64 | tr -d '\n')"
if ! git -c "http.extraheader=$AUTH" fetch --quiet origin "$HEAD_SHA" 2>/dev/null; then
  echo "warn: authenticated fetch failed, retrying anonymously" >&2
  git fetch --quiet origin "$HEAD_SHA"
fi
git checkout --quiet "$HEAD_SHA"

# Refresh the reviewer binary when its repo moved (cheap: shallow pull + build).
if git -C /work/pr-review pull --quiet --ff-only 2>/dev/null | grep -q .; then
  (cd /work/pr-review && go build -o /usr/local/bin/run-review ./cmd/run-review)
fi

exec flock -n /work/.lock run-review -pr "$PR" -workdir /work -repo /work/repo "$@"
