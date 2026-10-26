#!/usr/bin/env bash
# Writes the `conversions` of filex-convert's store listing
# (seed/official/filex-convert/listing.json) and its source note
# (seed/official/filex-convert/CONVERSIONS.md) from filex-convert's own
# conversion planner at a tag - never by hand.
#
#   bash scripts/gen-convert-conversions.sh <filex-convert-checkout> <tag>          write both files
#   bash scripts/gen-convert-conversions.sh <filex-convert-checkout> <tag> --check  compare, exit 1 on a difference
#
# Needs git, tar and Go (the version filex-convert's go.mod names; its module
# dependencies are downloaded). The checkout is only read: the tag is
# exported (`git archive`) into a temporary directory, the generator
# (scripts/convert-conversions/main.go.in) is copied in as
# tools/fapps-conversions and run there, because the planner lives in
# filex-convert's internal packages. The directory is removed afterwards.
set -euo pipefail

repo=${1:?usage: gen-convert-conversions.sh <filex-convert-checkout> <tag> [--check]}
tag=${2:?usage: gen-convert-conversions.sh <filex-convert-checkout> <tag> [--check]}
flag=${3:-}
case "$flag" in
  '' | --check) ;;
  *) echo "gen-convert-conversions.sh: unknown flag '$flag'" >&2; exit 2 ;;
esac

here=$(cd "$(dirname "$0")" && pwd)
seed=$(cd "$here/../seed/official/filex-convert" && pwd)
gen=$here/convert-conversions/main.go.in
[ -f "$seed/listing.json" ] || { echo "gen-convert-conversions.sh: $seed/listing.json is missing" >&2; exit 2; }

commit=$(git -c safe.directory='*' -C "$repo" rev-parse "$tag^{commit}")
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
git -c safe.directory='*' -C "$repo" archive --format=tar "$tag" | tar -x -C "$tmp"
mkdir -p "$tmp/tools/fapps-conversions"
cp "$gen" "$tmp/tools/fapps-conversions/main.go"

check=()
[ "$flag" = --check ] && check=(-check)
cd "$tmp"
go run ./tools/fapps-conversions -tag "$tag" -commit "$commit" \
  -listing "$seed/listing.json" -notes "$seed/CONVERSIONS.md" "${check[@]}"
