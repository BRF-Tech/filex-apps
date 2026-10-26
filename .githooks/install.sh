#!/usr/bin/env bash
# Point this clone at the repository's hooks (.githooks/). The setting lives
# in the clone's shared config, so every worktree of the clone uses the
# hooks of its own checkout from then on.
#
#   bash .githooks/install.sh            install
#   bash .githooks/install.sh --remove   back to .git/hooks
set -euo pipefail
repo=$(git rev-parse --show-toplevel)
if [ "${1:-}" = "--remove" ]; then
  git -C "$repo" config --unset core.hooksPath || true
  echo "core.hooksPath removed"
  exit 0
fi
git -C "$repo" config core.hooksPath .githooks
echo "core.hooksPath = .githooks (pre-push: gitleaks, scripts/checks/go.sh and web.sh in Docker)"
