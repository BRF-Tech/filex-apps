#!/usr/bin/env bash
# The Go checks: gofmt, go vet, go build, go test of backend/. Runs inside the
# Go image with backend/ as the working directory. One script for every place
# that checks - the pre-push hook (.githooks/pre-push) and CI
# (.github/workflows/ci.yml) - so they never drift apart.
set -e
echo "== go version: $(go version)"
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then echo "== gofmt: not formatted:"; echo "$unformatted"; exit 1; fi
echo "== go vet";   go vet ./...
echo "== go build"; go build ./...
echo "== go test";  go test -count=1 ./...
