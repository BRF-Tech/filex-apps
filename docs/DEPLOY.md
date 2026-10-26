# Running and developing the store

How a running store is kept: new versions, rollback, the validators at each filex release, the
operator commands, the official apps, the generated data files, and how the code is built and
tested. Setting a store up for the first time is [SELF-HOSTING.md](SELF-HOSTING.md); the
architecture and the decisions behind this page are [ARCHITECTURE.md](ARCHITECTURE.md).

The commands below use the example compose project of [`deploy/compose/`](../deploy/compose/)
(`docker compose exec app ...`, run next to its `docker-compose.yml`); with another setup, put the
store container's name in place of `app` (`docker exec <container> ...`).

## Contents

1. [Deploying a new version](#deploying-a-new-version)
2. [Rollback](#rollback)
3. [After a deploy: checks](#after-a-deploy-checks)
4. [Secrets and keys](#secrets-and-keys)
5. [Client addresses](#client-addresses)
6. [Validators](#validators)
7. [The plugin validator](#the-plugin-validator)
8. [Notifications: operator commands](#notifications-operator-commands)
9. [Official apps](#official-apps)
10. [filex's own file types](#filexs-own-file-types) (and [filex-convert's conversions](#filex-converts-conversions))
11. [Building and testing](#building-and-testing)
    - [The end-to-end stack](#the-end-to-end-stack)
    - [The pre-push hook](#the-pre-push-hook)

## Deploying a new version

1. Pick the release: an image `ghcr.io/brf-tech/filex-apps:<version>` (built by
   `.github/workflows/release.yml` from a `v<version>` tag), or one you build yourself from a commit
   (`docker build --build-arg VERSION=<commit> -t <your registry>/filex-apps:<commit> .`, the context
   from `git archive <commit>` rather than a working tree, so nothing uncommitted gets in). Never a
   moving tag such as `latest` in production: `/healthz` reports the version, and a rollback needs
   the previous one by name.
2. A dump first:
   `docker compose exec -T postgres pg_dump -U fapps --clean --if-exists fapps | gzip > backups/pre-<version>.sql.gz`.
3. The new version in `.env` (`FAPPS_VERSION`), then
   `docker compose pull app && docker compose up -d --no-deps app` (`--no-deps`: the database and
   Redis are not recreated). Migrations run at start.
4. Check: [After a deploy](#after-a-deploy-checks).
5. When the release changes what the index says about versions already listed (its notes say so; for
   example when a version's permissions became filex's review list), publish the index:
   `docker compose exec app /fapps-server publish-index` (serial + 1, audited `index.publish`, the same
   call as the administration page's **Publish now**). Nothing else publishes it until the next
   approval or the refresh before expiry.

The store needs nothing else from the host: it serves HTTP on `0.0.0.0:8080`, applies its migrations
at start, and builds its absolute URLs from `FAPPS_PUBLIC_URL` (else `APP_URL`), never from request
headers.

## Rollback

- **A bad release**: the previous version in `.env` and `docker compose up -d --no-deps app`.
- **Migrations are forward-only** (a file is never edited after it ran) and additive, so the previous
  code runs on the newer schema: the migration runner ignores applied migrations the binary does not
  know. A migration that drops or rewrites data is announced in its release notes; then restore the
  dump of step 2: `gunzip -c backups/pre-<version>.sql.gz | docker compose exec -T postgres psql -U fapps -d fapps`.

## After a deploy: checks

```sh
curl -fsS https://apps.example.com/healthz          # 200 {"status":"ok","version":...}: the deployed version
curl -sS  https://apps.example.com/readyz           # 200 without warnings, or 503 with the list of what is missing
curl -sSI https://apps.example.com/ | grep -i -E 'content-security-policy|x-frame-options'
curl -sS  https://apps.example.com/v1/keys.json     # three uses: index, artifact, license
fapps verify-index -key <your index public key> -min-serial <a serial you saw before> \
  https://apps.example.com/v1/index.json
curl -sS -X POST https://apps.example.com/v1/licenses/verify -H 'Content-Type: application/json' \
  -d '{"key":"nonsense","app":"some-app","instance_id":"probe"}'   # 200, a signed "invalid"; 503 data_key_missing without FAPPS_DATA_KEY
```

`fapps` is the operator CLI (`backend/cmd/fapps`: `go run ./cmd/fapps verify-index ...` from
`backend/`); `verify-index` checks the index the way a filex does: the signature against the key you
give it (never the keys the index lists), schema 1, not expired, the serial not below `-min-serial`.

`/readyz` lists in `warnings` what keeps a feature off without stopping the store, for example
`FAPPS_DATA_KEY is not set` (licenses, install links, saved instances, notifications), `no license
signing key`, a role check that cannot see its administration repository, or a variable of the removed
Keycloak sign-in that is still set. After a change to the sign-in, sign in once with GitHub.

## Secrets and keys

The store reads every secret from its environment (`backend/internal/config`), never from a file in
the repository. `fapps-server keygen` makes the store's own keys in one go
([SELF-HOSTING.md](SELF-HOSTING.md#make-the-stores-keys)); the GitHub credentials are yours to make.

| Variable | What | If it is lost or replaced |
|---|---|---|
| `FAPPS_DATA_KEY` | 32+ random bytes, base64: license-key HMACs, install links' sealed license keys, saved instances, notification channels (HKDF per use) | **Never regenerate it** on a store with data: every license key becomes `invalid`, every saved instance and webhook unreadable. A database backup is incomplete without it. Without it those features answer `503 data_key_missing` and `/readyz` warns; the rest works |
| `FAPPS_INDEX_KEY` + `_KEY_ID` | ed25519 seed: signs the index and the install intents | filex servers trust its public half; a new key is a new store to them (they ask their administrator again, or refuse it when configured) |
| `FAPPS_ARTIFACT_KEY` + `_KEY_ID` | signs each version's module, interface bundle and manifest digests, and storage plugin builds | filex servers with it in `FILEX_PLUGIN_TRUSTED_KEYS` refuse what a new key signs |
| `FAPPS_LICENSE_KEY` + `_KEY_ID` | signs license verdicts; listed in `/v1/keys.json` | as the index key, for license answers |
| `FAPPS_SESSION_KEY` | cookie signing and GitHub token sealing, 32+ random bytes, base64 | a new one signs everybody out, nothing else |
| `FAPPS_VAPID_PRIVATE_KEY` (+ `FAPPS_VAPID_PUBLIC_KEY`) | the Web Push key pair of the browser notifications | a new pair makes every browser turn its notifications on again; without it they are off and the webhooks work |
| `FAPPS_GITHUB_CLIENT_SECRET` | the GitHub OAuth App's secret | make a new one on GitHub |
| `FAPPS_GITHUB_TOKEN`, `FAPPS_ADMIN_TOKEN` | the optional background-read token; the role check's token when the roles come from a repository | make new ones on GitHub |
| the database password | `FAPPS_DB_PASSWORD` in the example compose | change it in Postgres and `.env` together |

Not secrets: the validators' password (`FAPPS_VALIDATOR_PASSWORD`: it guards filex instances that
hold no data and take no traffic from outside), the public keys and their fingerprints.
`FAPPS_DEMO_KEYS=1` (refused unless `APP_ENV` is `demo` or `development`) makes throw-away signing
keys that no real filex trusts, for the end-to-end stack and local development; never in production.

The signing keys are ed25519 seeds (hex). The **fingerprint** of a key is the lower-hex sha256 of its
32 raw public key bytes, what filex shows when it asks an administrator to trust a store:
`python3 -c "import hashlib,sys; print(hashlib.sha256(bytes.fromhex(sys.argv[1])).hexdigest())" <ed25519 hex>`.
Publishing them: [SELF-HOSTING.md](SELF-HOSTING.md#publish-your-keys-fingerprints).

**Rotation** (phase 1): a new key in the environment under a new key id makes the previous active key
of that use `retired` at once, and a retired key is refused if it comes back. filex servers that
trust the store see a key they did not trust and ask again (or refuse, when configured). Rotate only
when nothing needs the old key, and publish the new fingerprints first
([ARCHITECTURE.md](ARCHITECTURE.md#signatures-and-keys)).

## Client addresses

The store keys its rate limits by client address (sign-in starts 10/min, API 120/min) and takes that
address from `X-Forwarded-For` only when the peer is trusted: with `FAPPS_TRUSTED_PROXIES` empty, a
loopback or private-address peer, and only the rightmost hop. With a non-empty list the store walks
`X-Forwarded-For` from the right and skips every trusted hop (`clientAddr` in
`internal/httpapi/middleware.go`).

- **A reverse proxy on the same host or Docker network, nothing in front of it**: the store's peer is
  a private address (the Docker bridge) and the proxy sets `X-Forwarded-For` to the visitor: leave
  `FAPPS_TRUSTED_PROXIES` empty.
- **A CDN in front of the proxy**: the rightmost hop is then the CDN's edge, so every visitor would
  share one rate-limit bucket. Either have the proxy set `X-Forwarded-For` to the visitor's address
  from the CDN's own header (only for connections that come from the CDN), or list the CDN's address
  ranges in `FAPPS_TRUSTED_PROXIES`.

The store sets its own security headers (HSTS, CSP, `X-Frame-Options`, `nosniff`); do not add a
second HSTS header in the proxy.

## Validators

The store asks real filex servers for the install review (the dry run) of every submission
(ARCHITECTURE.md Q7): `filex-latest` runs the newest filex release, `filex-floor` the previous minor,
and `plugin-latest` (the storage plugin validator) the same image as `filex-latest`. At each filex
release:

1. `filex-latest` and `plugin-latest` to the new release, `filex-floor` to the previous minor, the
   image lines together (in your compose file, and in this repository's `docker-compose.yml`, the e2e
   stack's).
2. A dump, then `docker compose up -d --no-deps filex-latest filex-floor plugin-latest`.
3. [filex's own file types](#filexs-own-file-types) generated again for the same release, in a store
   release of their own.

Each filex image is about 0.8 GB: check the host's free disk first. The validators check new
submissions only; approved versions stay as they are.

An app that uses what a filex release added to the manifest declares that release in its `filex`
range; until `filex-latest` is that release its submission fails `no_validator_in_range`. Examples:
filex 0.55 added `ui.frame_package`, `ui.connect_blob` and `ui.print` (the permissions
`ui:frame-package`, `ui:connect-blob` and `ui:print`); 0.56 added `encrypted_folders` (the derived
permission `files:e2e-plaintext`: an app's interface edits a file of an unlocked encrypted folder)
and `co_edit` (`files:co-edit`: it edits a file together with other people through filex's relay).
The store knows the derived permissions whatever the validator (`resolve.IsDerivedPermission`); a
manifest never lists them in `permissions` (filex refuses it there). A version whose range admits only
one of the validators is checked on that one; the other reports `out_of_range`, which is normal.

## The plugin validator

Storage plugin submissions (migration `0010`, [ARCHITECTURE.md, "Storage
plugins"](ARCHITECTURE.md#storage-plugins)) are checked by `filex plugin-validator` (filex 0.55 and
later). Without one, the store runs as before and a storage plugin submission fails its dry run with
`validator_unavailable` (the start-up log warns `FAPPS_PLUGIN_VALIDATORS not set`). The block in
[`deploy/compose/docker-compose.yml`](../deploy/compose/docker-compose.yml) (and in this repository's
`docker-compose.yml`) is the one to use; what each part is for:

- `network_mode: none`: the plugin under test reaches nothing, and nothing reaches it; the build
  comes in, and the result goes out, through the `plugin-spool` volume the store mounts at
  `/spool/latest` (`FAPPS_PLUGIN_VALIDATORS=plugins=/spool/latest`).
- The image's entrypoint runs it as root (no `PUID`), which it needs to hand `in/` to the store's
  user (`65532`, distroless `nonroot`) and to start each plugin as `nobody` (`65534`). The
  capabilities are exactly those: handing the plugin its socket directory, starting it as its user,
  killing every process of that user between two runs (`KILL`), and clearing what that user owned
  (`DAC_OVERRIDE`, `FOWNER`).
- **What the validator refuses to start with** (it exits, and the container restarts): on Linux
  `--plugin-uid` and `--work-dir` are required; the plugin's user is never uid or gid 0, never the
  validator's own and never `--store-uid`; `--work-dir` is not shared (sticky or writable by anyone);
  and the spool must not be writable by the plugin's user. `nobody` must be used by nothing else in
  this container: every process of it is killed between two runs.
- **Ownership:** `/work` is root's, `0711` (the tmpfs `mode`; the validator sets it again): each run
  gets a root-owned `0711` directory in it, with the build `0555`, and the plugin only its socket
  directory `run/`, so it can start its build and never replace it, nor list the other runs. The spool
  volume is root's (`0755`, Docker's default for a new named volume); the validator hands `in/` to
  `65532` and keeps `out/` (`0755`) its own. A bind-mounted spool must be the same: not writable by
  `65534`.
- Two tmpfs: `/work` is mounted `exec` (Docker mounts a tmpfs `noexec` by default, and each build runs
  from it); `/tmp`, the plugin's scratch space, stays `noexec`, and what the plugin's user left there
  is removed after each run.
- The image's PID 1 is `tini`, which reaps what the validator kills; with another image, add
  `init: true`.
- The healthcheck: filex's image asks `http://127.0.0.1:5212/healthz`, which the plugin validator does
  not serve (no HTTP, no network), so without its own the container reads `unhealthy`. Health is the
  heartbeat instead: `status.json`, written every few seconds, younger than a minute.
- The caps sit inside each other: the store refuses a build over `FAPPS_MAX_BINARY_MB` (256) before
  the validator's `--max-binary-mb` (512) could, and waits 4 minutes for an answer, above the
  validator's own `--job-timeout` (3 minutes). Raise `FAPPS_MAX_BINARY_MB` past 512 only together with
  `--max-binary-mb`, or such builds fail on the validator instead of at the store.

Check: **Administration -> Validators -> Plugin validators** lists `plugins` as reachable with its
filex version and platform (`linux/amd64`); `GET /api/v1/admin/validators` answers it in
`plugin_validators`. A second plugin validator on an arm64 host would run the `linux/arm64` builds
too; with one on amd64, only the `linux/amd64` build is run and the others are pinned and signed (the
app page says so).

## Notifications: operator commands

A signed-in person chooses their notifications on **Notifications** (`/me/notifications`): browsers
(Web Push) and webhooks (generic JSON signed with HMAC-SHA256, Slack, Discord, Teams;
[ARCHITECTURE.md](ARCHITECTURE.md#notifications)). An operator can add a webhook for an account by
hand (the account must have signed in once), the header's value on standard input, never on the
command line:

```sh
docker compose exec -T app /fapps-server notify-webhook -login alice -name "Team chat" -format generic \
  -url 'https://hooks.example.com/notify?group=filex-apps' -header-name Authorization -events all -test < header-file
docker compose exec app /fapps-server notify-test -login alice      # a test to each of the account's webhooks
```

`notify-webhook` prints the webhook (its address's host only) and, with `-test`, the receiver's answer;
the generic format's signing secret is not printed (the page shows a new one when a webhook is added
there). The administrators' events reach an account only while it is an administrator.

When a delivery fails: the page shows each channel's last answer (HTTP status and why); the deliveries
are in `notification_deliveries` (status, attempts, `http_status`, `last_error`) and the server logs
`notify: delivery failed` with the delivery id and the HTTP status, never the address. A new filex
release is announced within ten minutes of the deploy or the validator update that brings it
(`notify: a newer filex release` in the log); the store never asks GitHub for it.

## Official apps

An **official, verified** publisher's tags can be listed without a `.filex-apps.json` in the
repository, with the operator import ([ARCHITECTURE.md, "Operator import (official
publisher)"](ARCHITECTURE.md#operator-import-official-publisher)): the listing comes from a folder
the operator gives, and consent is accepted by ownership (the publisher is official and verified, the
repository's owner is its bound GitHub owner). Every other check runs as for any submission, the dry
runs on every validator included. The commands run inside the store's container; it has no shell,
so a listing folder is copied in under a new name every time:

```sh
docker compose exec app /fapps-server publisher -id acme -name "Acme" \
  -github-owner acme-org -homepage https://acme.example
docker compose cp ./listings/acme-viewer app:/tmp/listing-acme-viewer-1
docker compose exec app /fapps-server import -publisher acme -repo acme-org/acme-viewer \
  -ref v1.0.0 -listing /tmp/listing-acme-viewer-1/listing.json -approve
```

`publisher` makes the publisher bound to the GitHub owner's numeric id active, verified and official
(creating it if needed). `import` queues the tag like any submission, waits for the workers, prints
every check and, with `-approve`, approves it as the operator account (which signs the version and
publishes the index). A tag that fails a check is not listed; the command exits with the check's code
and message. Import the tags of one app oldest first, so the newest is the last one approved and the
app's listing follows it. A listing folder holds `listing.json` (the `.filex-apps.json`
[schema](../schema/filex-apps.schema.json)), `description.md`, `icon.png` and `screenshots/`, its
paths relative to the folder.

⚠ There is no `help` command: an unknown first argument starts a second server inside the
container. The commands are `publisher`, `import`, `conversions`, `publish-index`, `notify-webhook`,
`notify-test`, `keygen` and `healthcheck`.

**The filex project's own apps.** [`seed/official/`](../seed/official/) holds the listings of BRF
Tech's apps (filex-sign, filex-convert, filex-drawio, filextext, filex-retro, filex-media-thumbs, the
office editor, the language packs) as the reference store lists them, and
[`seed/official/import.sh`](../seed/official/import.sh) runs the commands above for them (publisher
`brf-tech`, GitHub `BRF-Tech`), on the host of the store's container:

```sh
FAPPS_CONTAINER=<the store's container> bash seed/official/import.sh publisher
FAPPS_CONTAINER=<the store's container> bash seed/official/import.sh filex-sign v0.1.1 v0.2.0 v0.3.0
```

`import.sh <folder> <tag>...` copies the folder into the container, under a new name every run
(`/tmp/fapps-seed-<folder>-<time>-<pid>`: `docker cp` onto a directory that exists would put the new
copy inside the old one, whose listing the import would then read), and runs `import ... -approve`
once per tag. A new app there: its folder (`TestSeedListingsAreValid` checks the listing) and its
lines in `import.sh`'s `repo_of` and `app_of`. When the seed folder is copied to a server first, remove
the earlier copy before (`rm -rf /tmp/fapps-seed && scp -r seed/official host:/tmp/fapps-seed`): a
copy that exists takes the new one inside it.

The kinds of file a listed version works with are read from its manifest at approval; a store that
starts with versions approved before migration `0007` extracts theirs at start-up and logs it once
(`msg="file types: backfill" extracted=<n>`; ARCHITECTURE.md, "File-type search").

**What a version converts** is its listing's `conversions` (ARCHITECTURE.md, "File-type search"):
kept at approval when the listing declares them, and for a version that is listed already (a tag is
imported once; a second import is refused with `version_exists`) set by the `conversions` command
from a listing file, only for the version whose code the declaration was measured against. No index
is published (the index does not carry conversions):

```sh
FAPPS_CONTAINER=<the store's container> bash seed/official/import.sh conversions filex-convert 0.2.0
```

`import.sh conversions <folder> <version>...` copies the folder into the container and runs
`/fapps-server conversions -app <app> -version <version> -listing ...` once per version; it prints the
entries, pairs and the sha256 of the declaration before and after, or "unchanged". A listing without
`conversions` removes a version's declaration. Audited as `version.conversions`.

## filex's own file types

The catalog's file-type search says what filex does with a kind before any app is installed
("`.docx`: filex opens it with ONLYOFFICE"). That answer is
[`backend/internal/filetypes/filex-builtin.json`](../backend/internal/filetypes/filex-builtin.json),
embedded in the server and generated from one filex tag by
[`scripts/gen-filex-builtin.mjs`](../scripts/gen-filex-builtin.mjs); it is never edited by hand.
It reads a filex checkout with `git show <tag>:<path>` only (nothing is checked out), so any clone
with the tag will do. It is plain Node (no packages).

```sh
node scripts/gen-filex-builtin.mjs /path/to/filex v0.57.3 --check   # is the committed file what the tag says?
node scripts/gen-filex-builtin.mjs /path/to/filex v0.58.0           # write it for another release
```

- **When**: with each filex release, together with the validators' image tags, so the panel speaks
  for the filex people install. The file records `filex`, `tag` and `commit`, and the page names that
  version ("What filex 0.57.3 does with no app installed").
- **When it stops**: every list comes from one named place in filex (the preview's viewers and its
  Edit button, the server's rule of how a file is edited, the kinds ONLYOFFICE also opens, the
  thumbnail engine, the archive providers, the New menu, the settings, the default image's tools;
  `sources` in the file holds each path and its lines). When filex moves or renames one, the script
  stops and names it. Read the new code, change the anchor in the script, and keep what it claims to
  what the code does: a capability goes in only when the code shows filex doing it with nothing
  connected, or says what it needs (an ONLYOFFICE or draw.io server, ffmpeg, Ghostscript,
  ImageMagick, 7-Zip). Two examples: since filex 0.54 the office documents and the text the built-in
  editor saves are the server's rule, `backend/internal/editkind` (published as `edit_kinds` of
  `/api/files/capabilities`), not lists in the preview, so the script reads that rule from the Go code
  and stops if the copy filex's own tests hold (`backend/internal/api/handlers/testdata/rule-mirrors.json`)
  says otherwise; since 0.56 a viewer that draws a diagram from its text (`SOURCE_VIEWERS`, Mermaid's
  `.mmd` and `.mermaid`) is offered Edit, which opens that text in the built-in editor, and the script
  reads that list and claims the edit.
- **Then**: run the Go tests (`internal/filetypes` checks the file and the claims the catalog makes
  most) and look at a file-type search on the page.

### filex-convert's conversions

filex-convert's manifest names no kind (its Convert… action is offered on every file), so what it
converts into what is declared in its listing:
[`seed/official/filex-convert/listing.json`](../seed/official/filex-convert/listing.json)'s
`conversions`, with the source note [`CONVERSIONS.md`](../seed/official/filex-convert/CONVERSIONS.md)
beside it. Both are written by [`scripts/gen-convert-conversions.sh`](../scripts/gen-convert-conversions.sh)
from filex-convert's own conversion planner at a tag, never by hand: the tag is exported (`git archive`)
into a temporary directory, the generator
([`scripts/convert-conversions/main.go.in`](../scripts/convert-conversions/main.go.in)) is copied in and
run there with Go (filex-convert's planner is in its internal packages; its module's dependencies are
downloaded).

```sh
bash scripts/gen-convert-conversions.sh /path/to/filex-convert v0.2.0 --check   # is the committed declaration what the tag says?
bash scripts/gen-convert-conversions.sh /path/to/filex-convert v0.3.0           # write it for another release
```

- **What it claims**: for every format the converter detects (by extension) and every other format,
  the planner (`graph.Route`, the call the wizard makes for its target list) is asked whether a route
  exists with a given set of filex engines present; a pair is declared without `needs` only when it
  works with no engine at all (pure Go in the sandbox), else once per smallest set of engines that is
  enough. A format converted to itself is not declared. `CONVERSIONS.md` names the files and lines
  (`sources`) and lists every source format's targets by condition. When filex-convert moves one of
  them, the generator stops and names it.
- **When**: with each filex-convert release that is imported. The declaration belongs to the tag it
  was measured on: import the new tag with the regenerated listing (the declaration is kept at
  approval), or, for a version already listed, `import.sh conversions` with that version. Then
  capture the web mock again (`web/mock/convert-conversions.json`, `filetypes`'
  `TestTheMockConversionsAreTheServers` says when it drifted) and look at a `.docx` search.

## Building and testing

A change is tried before it is deployed: the pre-push checks on every push, CI on every pull request,
and the end-to-end stack below. Everything runs in Docker:

| Command | What |
|---|---|
| `docker build -t filex-apps .` | the store image from the [`Dockerfile`](../Dockerfile) (web build, Go build, distroless runtime) |
| `cd backend && bash ../scripts/checks/go.sh` | gofmt, go vet, go build, go test of `backend/` ([`scripts/checks/go.sh`](../scripts/checks/go.sh)) |
| `cd web && bash ../scripts/checks/web.sh` | pnpm install, `gen:api` (warns when the committed `schema.d.ts` is stale), build against the regenerated contract as the image does, unit tests ([`scripts/checks/web.sh`](../scripts/checks/web.sh)) |
| `bash scripts/integration.sh` | the backend's integration tests against PostgreSQL 16 and Redis 7 ([`scripts/integration.sh`](../scripts/integration.sh)) |
| `bash scripts/e2e.sh` | the end-to-end stack ([below](#the-end-to-end-stack)) |
| `REPO=. NAME=it FIXTURES=<cache dir> bash scripts/xcheck/validators-it.sh` | the validator package's tests against two real filex validators and recorded real tags (`RECORD=1` fills the cache from GitHub; `LIVE=1` also asks the real GitHub) |
| `FILEX_SRC=<filex checkout> OUT=<scratch dir> bash scripts/xcheck/run.sh` | the signature cross-check: the store's test vectors verified by filex's own `VerifyDetached` |

CI ([`.github/workflows/ci.yml`](../.github/workflows/ci.yml)) runs `go.sh`, `web.sh`,
`integration.sh` and gitleaks on every push to `main` and every pull request; the end-to-end stack is
[`e2e.yml`](../.github/workflows/e2e.yml), started by hand. A tag `v<version>` publishes the image
([`release.yml`](../.github/workflows/release.yml)).

### The end-to-end stack

`docker-compose.yml` + [`docker-compose.e2e.yml`](../docker-compose.e2e.yml), run by
[`scripts/e2e.sh`](../scripts/e2e.sh): the store, the two real validators and the plugin validator, a
Postgres and a Redis of its own, a test-only mock of GitHub (`e2e/mock-idp`), an HTTPS edge in place of
the reverse proxy (`https://fapps.e2e.test`), and Playwright with Chromium, Firefox and WebKit
(`e2e/`). Each run makes its own secrets and its own throw-away CA, and removes everything (`down -v`)
at the end; the report stays in `E2E_OUT` (`report/`, `results.json`, `ps.txt`, `stack.log`).
`E2E_BROWSERS=chromium`, `E2E_GREP=<regex>` and `E2E_CORRUPT_ASSETS=1` (every release asset one byte
off: the stack's own red test) narrow or invert a run. It signs with throw-away keys
(`FAPPS_DEMO_KEYS=1` from `.env.example`, with `APP_ENV=development`).

How "GitHub" works there: nothing leaves the machine. The validators and the store keep GitHub's
own addresses (`raw.githubusercontent.com`, `github.com/.../releases/download/...`,
`api.github.com`); `extra_hosts` points those names at a sidecar on `127.0.0.1:443` inside each one's
network namespace, which serves the fixture repositories (`e2e/fixtures`) over HTTPS with the run's
CA. filex reads them with `FILEX_PLUGIN_LOOPBACK_SOURCES=1` (the test validators only). The store
reads them with three settings that exist for development and this stack only, refused outside
`APP_ENV=development` (`e2e/stack.env`): `FAPPS_FETCH_ALLOW_PRIVATE=1`, `FAPPS_FETCH_CA_FILE` (the
run's CA) and `FAPPS_FETCH_ALLOW_HOSTS` with the mock's host added. HTTPS stays required. The sign-in
goes to the mock at `https://mock-idp:9443` (`FAPPS_GITHUB_OAUTH_URL`), which browsers and the store
reach by the same name. The role check reads the mock's private administration repository
`e2e-brf/filex-market` (`FAPPS_ADMIN_REPO` in `e2e/stack.env`; collaborators `e2e-admin` write,
`e2e-reviewer` triage, `e2e-nobody` read) through the app's sidecar, with the run's
`FAPPS_ADMIN_TOKEN`, which the mock and the sidecar get as `MOCK_GITHUB_ADMIN_TOKEN`. None of these
settings belongs in production.

A run against the real GitHub (`validators-it.sh` with `LIVE=1`) is separate and started by hand;
the end-to-end stack never needs the network beyond the image registries.

### The pre-push hook

[`.githooks/pre-push`](../.githooks/pre-push), installed with `bash .githooks/install.sh`: the pushed
commit is cloned from a git bundle into a named Docker volume (the working tree is never mounted),
then gitleaks runs over the pushed commits ([`.githooks/gitleaks.toml`](../.githooks/gitleaks.toml)),
then `scripts/checks/go.sh` and `scripts/checks/web.sh`, one container at a time, each limited by
`FAPPS_PREPUSH_CPUS` (default 3) and `FAPPS_PREPUSH_MEMORY` (default `8g`); the push goes ahead only if
all pass. The caches stay in the named volumes `fapps-prepush-gomod`, `fapps-prepush-gocache` and
`fapps-prepush-pnpm`; the commit's own volume is removed at the end. The images are `GO_IMAGE`
(default `golang:1.25`), `NODE_IMAGE` (default `node:22-bookworm-slim`) and `GITLEAKS_IMAGE`.
`FAPPS_PREPUSH_HOST=ssh:<host>` runs the same containers on another machine's Docker
(`DOCKER_HOST=ssh://<host>`). The hook has no skip switch.
