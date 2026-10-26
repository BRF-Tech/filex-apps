#!/usr/bin/env bash
# The end-to-end stack on this machine's Docker (or the Docker that
# DOCKER_HOST names): docker-compose.yml + docker-compose.e2e.yml, with
# .env.example, e2e/stack.env and a secrets file made for the run.
#
#   bash scripts/e2e.sh          build, up --wait, Playwright, logs, down -v
#   bash scripts/e2e.sh down     remove a stack an interrupted run left behind
#   bash scripts/e2e.sh ps       the stack's containers
#
# What the stack is (the store, the real filex validators, a mock of GitHub,
# an HTTPS edge, Playwright in Chromium, Firefox and WebKit): docs/DEPLOY.md
# "The end-to-end stack". Nothing leaves this machine but image pulls.
#
# Settings (environment):
#   E2E_BROWSERS        chromium,firefox,webkit (default) or a subset
#   E2E_GREP            a Playwright --grep pattern
#   E2E_CORRUPT_ASSETS  1: every release asset one byte off (the stack's own red test)
#   E2E_OUT             where the report, results.json, ps.txt and stack.log go
#                       (default: a new temporary directory, printed at the end)
#   E2E_PROJECT         compose project name (default fapps-e2e)
#   E2E_POSTGRES_IMAGE  default postgres:16.15-alpine
#   E2E_REDIS_IMAGE     default redis:7.4.11-alpine
#   GO_IMAGE            the Go build stage (default golang:1.25-bookworm)
#
# The run's secrets (database passwords, session and data keys, the GitHub
# client secret, the stand-in for the administration repository token, the
# mock's token key) are random, written to a mode-0600 file for the run and
# removed at the end; nothing here is ever a real credential. The store signs
# with throw-away keys (FAPPS_DEMO_KEYS=1 from .env.example, APP_ENV=development).
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
what=${1:-run}
case "$what" in
  run | down | ps) ;;
  *) echo "usage: bash scripts/e2e.sh [run|down|ps]" >&2; exit 2 ;;
esac

project=${E2E_PROJECT:-fapps-e2e}
case "$project" in
  '' | *[!a-z0-9_-]*) echo "e2e.sh: E2E_PROJECT: lower-case letters, digits, _ and - only" >&2; exit 2 ;;
esac
export E2E_POSTGRES_IMAGE=${E2E_POSTGRES_IMAGE:-postgres:16.15-alpine}
export E2E_REDIS_IMAGE=${E2E_REDIS_IMAGE:-redis:7.4.11-alpine}
export GO_IMAGE=${GO_IMAGE:-golang:1.25-bookworm}

rand_hex() { head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n'; }

secrets=$(mktemp)
chmod 600 "$secrets"
cleanup() { rm -f "$secrets"; }
trap cleanup EXIT
{
  echo "E2E_PG_SUPERPASS=$(rand_hex)"
  echo "E2E_DB_PASSWORD=$(rand_hex)"
  echo "FAPPS_SESSION_KEY=$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
  echo "FAPPS_DATA_KEY=$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
  echo "FAPPS_GITHUB_CLIENT_SECRET=$(rand_hex)"
  echo "FAPPS_ADMIN_TOKEN=github_pat_e2e_$(rand_hex)"
  echo "MOCK_TOKEN_KEY=$(rand_hex)"
} > "$secrets"

stack() {
  docker compose -p "$project" --project-directory "$repo" \
    -f "$repo/docker-compose.yml" -f "$repo/docker-compose.e2e.yml" \
    --env-file "$repo/.env.example" --env-file "$repo/e2e/stack.env" --env-file "$secrets" "$@"
}

if [ "$what" = ps ]; then
  stack ps -a
  exit 0
fi
if [ "$what" = down ]; then
  stack down -v --remove-orphans
  exit 0
fi

out=${E2E_OUT:-$(mktemp -d)}
mkdir -p "$out"
export E2E_OUT=$out

stack down -v --remove-orphans >/dev/null 2>&1 || true
echo "== e2e: browsers ${E2E_BROWSERS:-chromium,firefox,webkit}${E2E_GREP:+, grep $E2E_GREP}${E2E_CORRUPT_ASSETS:+, CORRUPT_ASSETS=$E2E_CORRUPT_ASSETS}"
echo "== e2e: build"
# --profile run: the Playwright runner sits in a profile, and a plain
# `compose build` skips it, so `compose run` would reuse the image of an
# earlier run with its old specs.
stack --profile run build
echo "== e2e: up"
rc=0
stack up -d --wait --wait-timeout 300 || rc=$?
if [ "$rc" = 0 ]; then
  echo "== e2e: playwright"
  stack run --rm playwright || rc=$?
else
  echo "== e2e: the stack did not become healthy (exit $rc)"
fi
stack ps -a > "$out/ps.txt" 2>&1 || true
stack logs --no-color --timestamps > "$out/stack.log" 2>&1 || true
echo "== e2e: down"
stack down -v --remove-orphans || true
echo "== e2e: report in $out"
echo "== exit $rc"
exit "$rc"
