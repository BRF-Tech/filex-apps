#!/usr/bin/env bash
# The backend's integration tests (`go test -tags integration`) against a real
# PostgreSQL 16 and Redis 7, all three in Docker on a network of their own:
#
#   bash scripts/integration.sh                        every package
#   bash scripts/integration.sh ./internal/store/...   some packages
#
# The tree (tracked and untracked files, minus what .gitignore drops) is
# copied into a volume of its own, so nothing of the working tree is mounted
# and any Docker will do (this machine's, or DOCKER_HOST's). The database and
# Redis live on tmpfs; the containers, the network and the tree's volume are
# removed at the end. Each test makes its own database (internal/db/dbtest).
# The validator package's tests skip here: they need two real filex
# validators and recorded tags (scripts/xcheck/validators-it.sh). CI runs this
# script as it is (.github/workflows/ci.yml).
#
# Images: GO_IMAGE (golang:1.25), IT_POSTGRES_IMAGE (postgres:16.15-alpine),
# IT_REDIS_IMAGE (redis:7.4.11-alpine). The Go caches are the named volumes
# fapps-it-gomod and fapps-it-gocache, kept between runs.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
go_image=${GO_IMAGE:-golang:1.25}
pg_image=${IT_POSTGRES_IMAGE:-postgres:16.15-alpine}
redis_image=${IT_REDIS_IMAGE:-redis:7.4.11-alpine}
id=fapps-it-$$
pw=$(head -c 18 /dev/urandom | od -An -tx1 | tr -d ' \n')
pkgs=("$@")
[ "${#pkgs[@]}" -gt 0 ] || pkgs=(./...)

dockerq() { MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*' docker "$@"; }
cleanup() {
  dockerq rm -f "$id-pg" "$id-redis" "$id-go" >/dev/null 2>&1 || true
  dockerq network rm "$id" >/dev/null 2>&1 || true
  dockerq volume rm -f "$id" >/dev/null 2>&1 || true
}
trap cleanup EXIT

want=$(git -C "$repo" -c core.quotepath=off ls-files -co --exclude-standard | wc -l | tr -d ' ')
dockerq volume create "$id" >/dev/null
for v in fapps-it-gomod fapps-it-gocache; do dockerq volume create "$v" >/dev/null; done
got=$(git -C "$repo" -c core.quotepath=off ls-files -z -co --exclude-standard |
  tar -C "$repo" --null -T - -cf - |
  dockerq run -i --rm -v "$id:/w" "$go_image" bash -ec 'tar -x -C /w; find /w -type f | wc -l') || {
  echo "integration.sh: could not copy the tree into volume $id" >&2
  exit 1
}
got=$(printf '%s' "$got" | tr -d ' \r\n')
if [ "$got" != "$want" ]; then
  echo "integration.sh: copied $got files, expected $want" >&2
  exit 1
fi
echo "== integration: $got files in volume $id"

dockerq network create "$id" >/dev/null
dockerq run -d --name "$id-pg" --network "$id" --tmpfs /var/lib/postgresql/data \
  -e POSTGRES_PASSWORD="$pw" "$pg_image" >/dev/null
dockerq run -d --name "$id-redis" --network "$id" "$redis_image" redis-server --save '' --appendonly no >/dev/null
for _ in $(seq 1 60); do
  if dockerq exec "$id-pg" pg_isready -h 127.0.0.1 -U postgres >/dev/null 2>&1 &&
    dockerq exec "$id-redis" redis-cli ping >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
dockerq exec "$id-pg" pg_isready -h 127.0.0.1 -U postgres >/dev/null

dockerq run --rm --name "$id-go" --network "$id" -v "$id:/w" \
  -v fapps-it-gomod:/go/pkg/mod -v fapps-it-gocache:/root/.cache/go-build \
  -w /w/backend -e GOFLAGS=-buildvcs=false \
  -e FAPPS_TEST_DATABASE_URL="postgres://postgres:$pw@$id-pg:5432/postgres?sslmode=disable" \
  -e FAPPS_TEST_REDIS_URL="redis://$id-redis:6379/0" \
  "$go_image" bash -ec 'echo "== go version: $(go version)"; echo "== go vet -tags integration"; go vet -tags integration ./...; echo "== go test -tags integration"; go test -tags integration -count=1 "$@"' _ "${pkgs[@]}"
