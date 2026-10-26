#!/usr/bin/env bash
# The web checks: pnpm install,
# gen:api (a stale schema.d.ts is a loud warning), build against the
# regenerated contract as the image does, and the unit tests. Runs inside the
# Node image with web/ as the working directory and the pnpm store at
# /pnpm-store. One script for the pre-push hook (.githooks/pre-push) and CI
# (.github/workflows/ci.yml).
set -e
corepack enable >/dev/null 2>&1
corepack prepare pnpm@9.12.0 --activate >/dev/null
pnpm config set store-dir /pnpm-store >/dev/null
echo "== pnpm install"; pnpm install --frozen-lockfile
before=$(sha256sum src/api/schema.d.ts | cut -d" " -f1)
echo "== pnpm gen:api"; pnpm gen:api
after=$(sha256sum src/api/schema.d.ts | cut -d" " -f1)
if [ "$before" != "$after" ]; then
  echo "== WARNING: web/src/api/schema.d.ts is stale against api/openapi.yaml."
  echo "== The frontend lane regenerates and commits it (pnpm gen:api). Building with the"
  echo "== regenerated file, as the image does: a page that no longer fits the contract fails below."
fi
echo "== pnpm build"; pnpm build
if node -e "process.exit(require(\"./package.json\").scripts?.test ? 0 : 1)"; then
  echo "== pnpm test"; pnpm test
else
  echo "== pnpm test: no test script yet"
fi
