#!/usr/bin/env bash
# Signature cross-check: the store's vectors (backend/internal/signing/testdata/vectors.json)
# must verify with filex's OWN plugin.VerifyDetached.
#
# Runs in Docker (the golang:1.25 image), from any machine with a filex checkout:
#   FILEX_SRC=<filex checkout> OUT=<scratch dir> bash scripts/xcheck/run.sh
# It assembles a throw-away Go module from filex's signature.go plus the two
# helpers it needs from manager.go, adds the test and the vectors, and runs
# `go test` in the golang:1.25 image.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
: "${FILEX_SRC:?FILEX_SRC must point at a filex checkout}"
: "${OUT:?OUT must name a scratch directory}"
[ -n "$OUT" ] && [ "$OUT" != "/" ] || exit 1

sig="$FILEX_SRC/backend/internal/plugin/signature.go"
mgr="$FILEX_SRC/backend/internal/plugin/manager.go"
[ -f "$sig" ] && [ -f "$mgr" ] || { echo "not a filex checkout: $FILEX_SRC" >&2; exit 1; }

rm -rf "$OUT/xcheck"
mkdir -p "$OUT/xcheck/plugin"
printf 'module xcheck\n\ngo 1.25.0\n' > "$OUT/xcheck/go.mod"
cp "$sig" "$OUT/xcheck/plugin/signature.go"
{
  printf 'package plugin\n\nimport (\n\t"crypto/ed25519"\n\t"encoding/base64"\n\t"encoding/hex"\n\t"fmt"\n\t"strings"\n)\n\n'
  sed -n '/^func parsePublicKey(/,/^}/p;/^func short(/,/^}/p' "$mgr"
} > "$OUT/xcheck/plugin/helpers_from_manager.go"
cp "$here/xcheck_test.go.in" "$OUT/xcheck/plugin/xcheck_test.go"
cp "$repo/backend/internal/signing/testdata/vectors.json" "$OUT/xcheck/plugin/vectors.json"

docker run --rm --name "xcheck-$$" -v "$OUT/xcheck:/w" -w /w -e GOFLAGS=-buildvcs=false \
  golang:1.25 go test -count=1 -v ./plugin/
