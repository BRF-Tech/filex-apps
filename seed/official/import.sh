#!/usr/bin/env bash
# Operator import of the official publisher's apps into a running store
# (docs/ARCHITECTURE.md "Operator import (official publisher)", docs/DEPLOY.md
# "Official apps"). The folders are the filex project's own apps (publisher
# brf-tech, GitHub BRF-Tech), as the reference store lists them; another store
# runs the same server commands for its own official publisher. Runs on the
# host of the store's container, from a copy of this folder:
#
#   bash import.sh publisher            make brf-tech (GitHub BRF-Tech) active,
#                                       verified and official
#   bash import.sh <folder> <tag>...    import those tags of the folder's
#                                       repository, oldest first, approving each
#                                       one that passes every check
#   bash import.sh conversions <folder> <version>...
#                                       set what those listed versions convert to
#                                       the folder's listing.json `conversions`
#                                       (none: removed); audited, no index
#                                       published (the index does not carry them)
#
# The listing folder is copied into the container, under a name of its own for
# every run (/tmp/fapps-seed-<folder>-<time>-<pid>): the runtime image has no
# shell, so a copy stays until the container is recreated, and `docker cp`
# onto a directory that exists puts the new copy INSIDE it - a second import
# under one fixed name read the first run's listing.json. The
# store's own binary does the work: the whole pipeline, then approval
# and the signed index. A tag that fails a check stops the run with the
# check's code and message; nothing about it is listed.
set -euo pipefail

container=${FAPPS_CONTAINER:-fapps-app}
here=$(cd "$(dirname "$0")" && pwd)

repo_of() {
  case "$1" in
    filex-sign) echo BRF-Tech/filex-sign ;;
    filex-convert) echo BRF-Tech/filex-convert ;;
    filextext-app) echo BRF-Tech/filextext-app ;;
    filex-drawio) echo BRF-Tech/filex-drawio ;;
    filex-retro) echo BRF-Tech/filex-retro ;;
    filex-media-thumbs) echo BRF-Tech/filex-media-thumbs ;;
    filex-office-editor) echo BRF-Tech/filex-office-editor ;;
    filex-lang-es) echo BRF-Tech/filex-lang-es ;;
    filex-lang-de) echo BRF-Tech/filex-lang-de ;;
    filex-lang-fr) echo BRF-Tech/filex-lang-fr ;;
    *) return 1 ;;
  esac
}

# app_of is the store's name of the folder's app: its manifest's `name`.
app_of() {
  case "$1" in
    filex-sign) echo sign ;;
    filex-convert) echo convert ;;
    filextext-app) echo filextext ;;
    filex-drawio) echo drawio ;;
    filex-retro) echo retro ;;
    filex-media-thumbs) echo media-thumbs ;;
    filex-office-editor) echo office-editor ;;
    filex-lang-es) echo lang-es ;;
    filex-lang-de) echo lang-de ;;
    filex-lang-fr) echo lang-fr ;;
    *) return 1 ;;
  esac
}

what=${1:?usage: import.sh publisher | import.sh <folder> <tag>... | import.sh conversions <folder> <version>...}
shift

if [ "$what" = publisher ]; then
  docker exec "$container" /fapps-server publisher -id brf-tech -name "BRF Tech" \
    -github-owner BRF-Tech -homepage https://github.com/BRF-Tech
  exit 0
fi

if [ "$what" = conversions ]; then
  folder=${1:?usage: import.sh conversions <folder> <version>...}
  shift
  repo_of "$folder" >/dev/null || { echo "import.sh: unknown folder '$folder'" >&2; exit 2; }
  [ "$#" -gt 0 ] || { echo "import.sh: name at least one version" >&2; exit 2; }
  [ -f "$here/$folder/listing.json" ] || { echo "import.sh: $here/$folder/listing.json is missing" >&2; exit 2; }
  app=$(app_of "$folder")
  dest="/tmp/fapps-seed-$folder-$(date +%s)-$$"
  docker cp "$here/$folder" "$container:$dest"
  for version in "$@"; do
    echo "== $app $version"
    docker exec "$container" /fapps-server conversions -app "$app" -version "$version" -listing "$dest/listing.json"
  done
  exit 0
fi

repo=$(repo_of "$what") || { echo "import.sh: unknown folder '$what'" >&2; exit 2; }
[ "$#" -gt 0 ] || { echo "import.sh: name at least one tag" >&2; exit 2; }
[ -f "$here/$what/listing.json" ] || { echo "import.sh: $here/$what/listing.json is missing" >&2; exit 2; }

dest="/tmp/fapps-seed-$what-$(date +%s)-$$"
docker cp "$here/$what" "$container:$dest"
for tag in "$@"; do
  echo "== $repo $tag"
  docker exec "$container" /fapps-server import -publisher brf-tech -repo "$repo" -ref "$tag" \
    -listing "$dest/listing.json" -approve
done
