# Changelog

Each release is a signed tag `v<version>` and the image `ghcr.io/brf-tech/filex-apps:<version>`
([DEPLOY.md](docs/DEPLOY.md#deploying-a-new-version)). Migrations are listed when a release has
them; a release without one runs on the previous schema and the previous release runs on its schema.

## [0.1.1] - 2026-11-08

No migration. Deploy as usual; rolling back to 0.1.0 needs nothing but the image.

### Changed

- **GitHub's files and release assets through the REST API.** With `FAPPS_GITHUB_TOKEN` set, the
  pipeline reads the files at the tag's commit (the manifest, `.filex-apps.json`, the description,
  the images) with `GET /repos/{o}/{r}/contents/{path}?ref=<commit>` and the release assets a
  manifest names on `github.com` (a module, an interface bundle, a storage plugin's feed and builds)
  with `GET /repos/{o}/{r}/releases/assets/{id}`, instead of the anonymous `raw.githubusercontent.com`
  and `github.com/.../releases/download/...` addresses. The bytes and the recorded addresses are the
  same; the token stays on `api.github.com` (the redirect to the asset host goes without it). On
  2026-11-08 GitHub's anonymous downloads timed out or answered 504 for hours while the API
  answered, and four imports failed. Without a token, or when GitHub refuses or limits it, the reads
  are the anonymous ones, as before.
- **A submission waits out an outage.** Its jobs (resolve, dry run) are tried up to nine times
  instead of five, about 65 minutes in all, and each background GitHub read three times in the call
  (2 s and 6 s apart). A validator that GitHub did not serve the release to (filex's `fetch_failed`
  with a 5xx or 429 status) is retried like an unreachable one; a 404 stays a verdict. After the last
  try, `resolve_error` and `dry_run_error` say how many tries over how long and the last error.
- **An operator import is never left half way.** `fapps-server import` waits up to `-timeout`
  (default `2h`, was `30m`) and prints each failed try of its jobs. When the timeout passes or it is
  interrupted, it withdraws the submission (audited `submission.withdraw`), so the tag can be
  imported again; before, the submission stayed in the pipeline, unapproved, and blocked the tag.
  `seed/official/import.sh` passes `FAPPS_IMPORT_TIMEOUT` as `-timeout`.
- **filex's own file types from filex 0.59.0** (`filex-builtin.json`, the validators' release): the
  same 160 kinds, the settings' new lines. The file records the tag's commit in
  [BRF-Tech/filex](https://github.com/BRF-Tech/filex), so `gen-filex-builtin.mjs --check` passes on
  any public clone.

## [0.1.0] - 2026-10-26

The first open release: the store as the reference store (`https://apps.filex.sh`) runs it, under
the MIT license.
