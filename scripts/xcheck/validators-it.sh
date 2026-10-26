#!/usr/bin/env bash
# Integration tests of backend/internal/validator against two REAL filex
# validators: ghcr.io/brf-tech/filex v0.57.3 ("latest") and v0.56.0
# ("floor"), the versions docs/ARCHITECTURE.md Q7 names (the same image
# lines as docker-compose.yml; LATEST_IMAGE / FLOOR_IMAGE override them for
# one run).
#
# Runs in Docker, from any machine:
#   REPO=<filex-apps checkout> NAME=<run name> FIXTURES=<cache dir> [RECORD=1] [LIVE=1] \
#   [PREFIX=side-apps-val-it] [GOMODCACHE_DIR=<dir>] [GOCACHE_DIR=<dir>] \
#   bash scripts/xcheck/validators-it.sh
#
# FIXTURES holds the recorded contents of the real tags in
# backend/internal/validator/testdata/fixtures.json, one file per sha256,
# outside the repository (tens of MB of modules). RECORD=1 fills what is
# missing from GitHub (downloads only, checked against the recorded hashes).
#
# The two validators and the Go test share ONE network namespace, so the
# recorded fixtures the test serves on 127.0.0.1 are loopback to filex too.
# Only these throw-away validators run with FILEX_PLUGIN_LOOPBACK_SOURCES=1;
# their data is on tmpfs, their admin password is random per run, and every
# container is removed on exit. LIVE=1 also runs TestLive_* against real
# GitHub (anonymous; about eight API calls) - only when started by hand.
set -euo pipefail

: "${REPO:?REPO must point at a filex-apps checkout}"
: "${NAME:?NAME names the run}"
: "${FIXTURES:?FIXTURES must name the recorded contents directory}"
LIVE=${LIVE:-0}
RECORD=${RECORD:-0}
mkdir -p "$FIXTURES"
PREFIX=${PREFIX:-fapps-it}
[ -d "$REPO/backend" ] || { echo "not a filex-apps checkout: $REPO" >&2; exit 1; }
case "$NAME$PREFIX" in *[!A-Za-z0-9_.-]*) echo "NAME and PREFIX: letters, digits, . _ - only" >&2; exit 1 ;; esac

PW=$(head -c 18 /dev/urandom | od -An -tx1 | tr -d ' \n')
LATEST="$PREFIX-latest"
FLOOR="$PREFIX-floor"
TESTC="$PREFIX-go-$NAME"
cleanup() { docker rm -f "$LATEST" "$FLOOR" "$TESTC" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

validator() {
  local name=$1 image=$2 listen=$3 net=$4
  docker run -d --name "$name" $net --memory 768m --tmpfs /data:rw,size=512m \
    -e FILEX_LISTEN="$listen" -e FILEX_ADMIN_EMAIL=validator@local -e FILEX_ADMIN_PASSWORD="$PW" \
    -e FILEX_PLUGINS_DISABLED=1 -e FILEX_APP_PLUGINS_DISABLED=0 -e FILEX_PLUGIN_LOOPBACK_SOURCES=1 \
    "$image" >/dev/null
}
validator "$LATEST" "${LATEST_IMAGE:-ghcr.io/brf-tech/filex:v0.57.3}" 127.0.0.1:5212 ""
validator "$FLOOR" "${FLOOR_IMAGE:-ghcr.io/brf-tech/filex:v0.56.0}" 127.0.0.1:5213 "--network container:$LATEST"

caches=()
[ -n "${GOMODCACHE_DIR:-}" ] && caches+=(-v "$GOMODCACHE_DIR:/go/pkg/mod")
[ -n "${GOCACHE_DIR:-}" ] && caches+=(-v "$GOCACHE_DIR:/root/.cache/go-build")

status=0
docker run --rm --name "$TESTC" --network "container:$LATEST" -v "$REPO:/w" -v "$FIXTURES:/fixtures" "${caches[@]}" \
  -w /w/backend -e GOFLAGS=-buildvcs=false -e GOTOOLCHAIN=local \
  -e FAPPS_IT_VALIDATORS=latest=http://127.0.0.1:5212,floor=http://127.0.0.1:5213 \
  -e FAPPS_IT_EMAIL=validator@local -e FAPPS_IT_PASSWORD="$PW" -e FAPPS_IT_LIVE_GITHUB="$LIVE" \
  -e FAPPS_IT_FIXTURES=/fixtures -e FAPPS_IT_RECORD="$RECORD" \
  golang:1.25 bash -c 'for i in $(seq 1 90); do curl -sf 127.0.0.1:5212/healthz >/dev/null && curl -sf 127.0.0.1:5213/healthz >/dev/null && break; sleep 1; done; echo "validators up after ${i}s"; go vet -tags integration ./internal/validator/ && go test -tags integration -count=1 -v -run "Integration|Live" ./internal/validator/' || status=$?

echo "--- $LATEST (app plugins, warnings)"
docker logs "$LATEST" 2>&1 | grep -E 'app-plugins|level=(WARN|ERROR)' | tail -25 || true
echo "--- $FLOOR (app plugins, warnings)"
docker logs "$FLOOR" 2>&1 | grep -E 'app-plugins|level=(WARN|ERROR)' | tail -25 || true
exit "$status"
