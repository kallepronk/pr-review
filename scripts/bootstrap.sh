#!/usr/bin/env bash
# Runs once inside a fresh sprite. Idempotent: guarded by /work/.bootstrapped.
# Env: REPO (owner/name of the repo under review), PR_REVIEW_REPO (owner/name
# of this tool's repo, public), GH_TOKEN (for cloning REPO if private).
set -euo pipefail

if [ -f /work/.bootstrapped ]; then exit 0; fi
mkdir -p /work

# pi: small model-agnostic agent harness for the tool-using tickets. Node is
# preinstalled on sprites; pi reads ANTHROPIC_API_KEY from the environment.
npm install -g --ignore-scripts @earendil-works/pi-coding-agent

# Build run-review from source; Go is preinstalled on sprites.
command -v go >/dev/null || { echo "go not found in sprite" >&2; exit 1; }
git clone --depth 1 "https://github.com/${PR_REVIEW_REPO}.git" /work/pr-review
(cd /work/pr-review && go build -o /usr/local/bin/run-review ./cmd/run-review)

# Clone the repo under review. Token only needed for private repos.
if [ -n "${GH_TOKEN:-}" ]; then
  git clone --filter=blob:none "https://x-access-token:${GH_TOKEN}@github.com/${REPO}.git" /work/repo
  git -C /work/repo remote set-url origin "https://github.com/${REPO}.git"  # drop token from disk
else
  git clone --filter=blob:none "https://github.com/${REPO}.git" /work/repo
fi

touch /work/.bootstrapped
# Checkpoint so a broken round can be rolled back to a clean, tooled sprite.
sprite-env checkpoint create --comment "bootstrap" 2>/dev/null || true
