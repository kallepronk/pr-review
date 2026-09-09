#!/usr/bin/env bash
# One review round inside the sprite. Called by the workflow via sprite exec.
# Env: PR (owner/repo#N), HEAD_SHA, GH_TOKEN, a model key, optional PRREVIEW_MODEL,
# optional REVIEW_ARGS (e.g. "-force -full" from the /review command).
set -euo pipefail

# Non-interactive exec has no login PATH; npm's global bin (pi lives there) must be added.
export PATH="$(npm prefix -g)/bin:$PATH"

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

# Rebuild the reviewer; the workflow already pulled /work/pr-review. Go's build
# cache makes this a no-op when nothing changed.
(cd /work/pr-review && go build -o /usr/local/bin/run-review ./cmd/run-review)

# shellcheck disable=SC2086  # REVIEW_ARGS is a deliberate word-split flag list
exec flock -n /work/.lock run-review -pr "$PR" -workdir /work -repo /work/repo ${REVIEW_ARGS:-} "$@"
