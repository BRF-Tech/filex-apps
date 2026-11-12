# Changelog

Each release is a signed tag `v<version>` and the image `ghcr.io/brf-tech/filex-apps:<version>`
([DEPLOY.md](docs/DEPLOY.md#deploying-a-new-version)). Migrations are listed when a release has
them; a release without one runs on the previous schema and the previous release runs on its schema.

## [0.1.2] - 2026-11-12

No migration. Deploy as usual; rolling back to 0.1.1 needs nothing but the image.

### Added

- **Apps that are set up after they are installed (filex 0.60.0).** The pipeline's `manifest` check
  holds the manifest's `routes`, `setup` and `link_shortener` blocks, and the permissions and views
  they lean on, to filex's own rules: filex's public Go module (`github.com/brf-tech/filex/backend`
  v0.60.0) runs its `plugintest.InspectManifest` on those blocks, and a breach fails the submission
  with filex's sentence (`manifest_unreadable`). The rest of a manifest is still the validators' to
  judge. The app page has a "Setting it up" section: the app's own setup step, its own addresses
  (the setup asks where they live), the link-shortener role, the warning before its data is erased,
  and what an administrator grants inside it (`user_permissions`, with who holds each until then);
  the API's version detail carries it as `setup`. The permissions `data`, `public_routes` and
  `link_shortener` have their own pictures.

### Changed

- **Validators: filex 0.60.0 (`latest`, `plugin-latest`) and 0.59.1 (`floor`)** in the example
  compose, the end-to-end stack and `validators-it.sh`, as on the reference store. Since filex 0.59.1
  a dry run reads the manifest alone (`"from_manifest": true`) and downloads nothing: the module's
  and the interface's hashes it reports are the manifest's pins. The bytes are checked by the
  resolver's `artifacts` step and by filex's install, which refuses a file that does not hash to its
  pin; a release asset swapped after the store resolved it no longer fails the dry run.
- **filex's own file types from filex 0.60.0** (`filex-builtin.json`): the same 160 kinds, five
  sources on new lines.

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
