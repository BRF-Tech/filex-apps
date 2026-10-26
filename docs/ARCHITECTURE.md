# filex Apps store - architecture

Status: **phase 1 and the paid-app round ([Licensing](#licensing-paid-apps), [Install
links](#install-links-magic-link)) are built**; the reference store runs them at
`https://apps.filex.sh` since 2026-10-05 ([Deployment](#deployment)), and anyone can run a store of
their own ([SELF-HOSTING.md](SELF-HOSTING.md)). Payments are not built, on purpose
([Phases](#phases)). This document records the decisions taken, the contract the code is built
against ([`api/openapi.yaml`](../api/openapi.yaml), [`backend/migrations/`](../backend/migrations/)),
and the questions and their answers ([Open questions](#open-questions)). Where this document and the
OpenAPI file disagree, the OpenAPI file is right and this document is fixed.

## Contents

1. [What the store is](#what-the-store-is)
2. [Decisions already taken](#decisions-already-taken)
3. [Components](#components)
4. [Data model](#data-model)
5. [Identity and access](#identity-and-access)
6. [Submission pipeline](#submission-pipeline)
    - [Storage plugins](#storage-plugins)
7. [Review and policy](#review-and-policy)
8. [Operator import (official publisher)](#operator-import-official-publisher)
9. [The signed index](#the-signed-index)
10. [Signatures and keys](#signatures-and-keys)
11. [Media mirror](#media-mirror)
12. [Yank and revoke](#yank-and-revoke)
13. [What a filex reads](#what-a-filex-reads)
14. [Licensing (paid apps)](#licensing-paid-apps)
15. [Install links (magic link)](#install-links-magic-link)
    - [Instance connections](#instance-connections)
16. [File-type search](#file-type-search)
17. [Notifications](#notifications)
18. [Security](#security)
19. [Configuration](#configuration)
20. [Deployment](#deployment)
21. [Web interface and reuse of filex components](#web-interface-and-reuse-of-filex-components)
22. [Testing](#testing)
23. [Phases](#phases)
24. [Open questions](#open-questions)

## What the store is

A catalog of filex **apps**, **language packs** and, from migration 0010, **storage plugins**:
publishers submit a public GitHub repository at a tag, the store checks it the way filex itself would install it, a reviewer approves it, and the store
publishes a **signed index** that filex instances can trust.

In scope: the catalog web interface, the publishing pipeline, review and administration, the signed
index and its keys, the media mirror, yank and revoke, and the API a filex client will read.

Out of scope for this repository's first phases: the filex side (Admin, Apps, a Store tab, the
page a magic link opens, update notices: in the filex repository), ratings, reviews by users,
payments (the store shows a price and checks license keys, it takes no money). Storage plugins,
filex's other plugin class, were out of scope until filex 0.55: they are listed now, checked
on a plugin validator ([Storage plugins](#storage-plugins)).

## Decisions already taken

| Decision | Source |
|---|---|
| The store is a **dynamic application**, not a static site on GitHub Pages | owner, 2026-10-03 |
| Stack: **Go + Vue 3**, like filex; **PostgreSQL** | owner, 2026-10-03 |
| Repository: code, documentation and commits are English, written to be open. **Open source under MIT**, one open repository (`BRF-Tech/filex-apps`) where development happens | owner, 2026-10-03; MIT and the open repository decided on 2026-10-26 |
| First deployment: a **demo** (2026-10-04), retired the next day for the reference store at `apps.filex.sh` ([Deployment](#deployment)) | owner, 2026-10-03; retired by the owner on 2026-10-05 |
| Everyone signs in with **GitHub OAuth** (for a publisher it also proves repository ownership). Reviewer and administrator roles come from the operator: GitHub user ids in the environment, or the account's permission on a private **administration repository**, read at every sign-in ([Roles](#roles-the-administration-repository), [Q14](#open-questions)). The first plan's Keycloak OIDC sign-in for staff is removed | owner, 2026-10-03; roles from GitHub and Keycloak removed by the owner on 2026-10-04; the id list on 2026-10-26 |
| **Anyone can run a store**: its name, logo and operator details, and its staff, come from the environment; `fapps-server keygen` makes its keys; a production compose example, CI and a published image ([SELF-HOSTING.md](SELF-HOSTING.md)) | owner, 2026-10-26 |
| The filex client side comes later; an "automatic installation with a magic link" idea will be described by the owner. This document only reserves the place for it | owner, 2026-10-03; described and built 2026-10-04, below |
| **Paid apps**: the store generates license keys, keeps them (as an HMAC) and is the channel that checks them; **Install** makes a one-time magic link to the person's own filex, which may be saved (encrypted) for next time. No payment in this phase | owner, 2026-10-04 ([Licensing](#licensing-paid-apps), [Install links](#install-links-magic-link)) |
| Still valid from the earlier plan (A2): the signature scheme of filex's `plugin.VerifyDetached`; the index format with `schema`, `serial`, `expires_at`, `keys`, `publishers`, `apps`, `versions`, `yanked`, `revoked`, `security`; two keys (index + artifact); `expires_at` 60 days; validation by a **real filex's dry run** at the newest version and at the lower end of the supported range; the policy and the first entries | plan A2 (K3, K5, K6, A2.6, A2.7) |
| Replaced from the earlier plan: the PR-based publishing flow and the static showcase - a web interface does both | owner, 2026-10-03 |
| The open questions Q1-Q13 were answered by taking the recommendations; Q11 (license) was left for later and answered on 2026-10-26: MIT ([Open questions](#open-questions)) | owner, 2026-10-03 and 2026-10-26 |

## Components

```
                    GitHub (OAuth sign-in, REST API, raw, release assets,
                          ^  the administration repository's permissions)
                          | allow-listed HTTPS only
 browser --- HTTPS ---> [ fapps server (Go, one process) ]
 (SPA)                    | - HTTP API + embedded SPA                 
 filex  --- HTTPS ---->   | - sessions, CSRF, rate limits             --- Postgres (its own; all state)
 (index)                  | - pipeline workers (Postgres job queue)   --- Redis (its own; rate limits only)
                          | - index publisher + signer (keys in env)
                          | - media mirror
                          +--- HTTP (compose default network) ---> filex-latest  (ghcr.io/brf-tech/filex:v0.57.3)
                          |                                   ---> filex-floor   (ghcr.io/brf-tech/filex:v0.56.0)
                          +--- shared folder (no network) -------> plugin-latest (ghcr.io/brf-tech/filex:v0.57.3)
```

| Component | What it does | Where |
|---|---|---|
| **Server** | One Go binary (`backend/cmd/server`): the JSON API, sign-in, the pipeline workers, the index publisher, the media mirror, the SPA. One image, one process | `backend/` |
| **Web interface** | Vue 3 + Vite + TypeScript SPA: catalog, app and publisher pages, Install (the magic link), the publisher's area (submit, status, price and licenses), the saved filex instances, the review queue, administration ([Web interface](#web-interface-and-reuse-of-filex-components)) | `web/`, embedded at `backend/internal/webui/dist` at image build |
| **PostgreSQL** | Everything durable: accounts, publishers, apps, versions, submissions, checks, reviews, media bytes, public keys, every signed index, the job queue, the audit log | the example compose's `postgres` (PostgreSQL 16, database `fapps`); the e2e stack has one of its own |
| **Redis** | Rate-limit counters only. Nothing is lost when it is empty; without it an in-process limiter stands in | the example compose's `redis` (Redis 7) |
| **Validators** | Real filex instances the store asks for the install review (`POST /api/admin/app-plugins?dry_run=1`), so the rules a manifest must satisfy are written once - in filex. No data (tmpfs), no ingress, reachable only from the server | compose services `filex-latest`, `filex-floor` |
| **Plugin validators** | `filex plugin-validator` (filex 0.55 and later) runs a storage plugin's build for its own platform - filex's handshake, describe and conformance probes - in a container with **no network**, answering through a folder it shares with the server ([Storage plugins](#storage-plugins)) | compose service `plugin-latest` (optional: without it storage plugin submissions stop at the dry run) |
| **Signer** | `internal/signing`: ed25519 with filex's scheme. Keys come from the environment (`FAPPS_INDEX_KEY`, `FAPPS_ARTIFACT_KEY`, `FAPPS_LICENSE_KEY`); the e2e stack and local development may generate throw-away keys (`FAPPS_DEMO_KEYS=1`). Behind an interface so an offline or hardware signer can replace it | in the server |

### Backend packages (the lanes' file boundaries)

| Package | Responsibility | Lane |
|---|---|---|
| `cmd/server` | wiring, config, start-up, graceful stop; the operator commands `keygen` (a new store's keys, [SELF-HOSTING.md](SELF-HOSTING.md#make-the-stores-keys)), `publisher`, `import`, `conversions` and `publish-index` ([Operator import](#operator-import-official-publisher)), `notify-webhook` and `notify-test` ([Notifications](#notifications)) | backend |
| `cmd/fapps` | operator CLI: `keygen`, `sign`, `verify-index`, `vectors` | validator |
| `internal/config` | every environment variable (one place) | architect, then backend |
| `internal/domain` | shared types (**contract**) | architect; changes are coordinated |
| `internal/db` | pgx pool, migration runner (embedded `migrations/*.sql`), transactions, advisory locks | backend |
| `internal/store` | queries per aggregate (accounts, publishers, apps, versions, submissions, media, keys, index releases, jobs, audit) | backend |
| `internal/httpapi` | router, middleware (session, CSRF, origin check, rate limit, security headers, request log), handlers per tag of the OpenAPI file | backend |
| `internal/session`, `internal/auth/github`, `internal/auth/adminrepo` | cookie sessions, the GitHub sign-in, the store roles from the administration repository | backend |
| `internal/pipeline` | the submission state machine and job workers; calls the two **ports** (`ports.go`, contract) | backend |
| `internal/safehttp` | the guarded outbound client (allow list, public addresses only, redirects, size and time caps) | validator |
| `internal/github` | GitHub REST and raw reads: repository, permission, tag to commit, file at commit, license, release assets | validator |
| `internal/consent` | parses and validates `.filex-apps.json` | validator |
| `internal/resolve` | implements `pipeline.Resolver` | validator |
| `internal/validator` | implements `pipeline.DryRunner` against the filex validators | validator |
| `internal/plugincheck` | implements `pipeline.DryRunner` for storage plugins: hands each build to a plugin validator through its spool folder and judges the answer ([Storage plugins](#storage-plugins)) | validator |
| `internal/filexrange` | filex's version-range syntax (comparators joined by spaces, alternatives by `\|\|`), semver order | validator |
| `internal/signing` | the signature scheme (**done**, with independent test vectors) | architect, then validator |
| `internal/index` | canonical index bytes, build from the database, sign, store, serve (schema in `schema.go`: **contract**) | backend |
| `internal/media` | image fetch, sniff, decode-size check, store, serve | backend |
| `internal/markdown` | an app's description from Markdown to safe HTML (goldmark without raw HTML, then a bluemonday allow list) | backend |
| `internal/ratelimit` | fixed-window counters in Redis, in the process when Redis is not configured or fails | backend |
| `internal/audit` | append-only hash-chained audit rows | backend |
| `internal/datakey` | the keys derived from `FAPPS_DATA_KEY` (HKDF per use: license-key HMAC, install-intent and instance AEADs, the instances' keyed index) | backend |
| `internal/signedjson` | canonical compact JSON and the signed envelope (license verdicts, install intents) | backend |
| `internal/licensing` | prices, license keys, the signed license check | backend |
| `internal/install` | install intents, the magic link, saved instances | backend |
| `internal/filetypes` | the kinds of file an app works with, read from its manifest, and what its listing says it converts (`convert`); what filex does by itself (`filex-builtin.json`); reading a search as file types and matching ([File-type search](#file-type-search)) | backend |
| `internal/notify` | notifications: the events and who hears them, the messages' words, the four webhook formats and their signature, Web Push (RFC 8291 encryption, RFC 8292 VAPID), the fan-out and delivery worker, the newest filex release ([Notifications](#notifications)) | backend |
| `internal/webui` | the embedded SPA and its fallback | frontend (build output only) |

## Data model

The schema is the migrations in [`backend/migrations/`](../backend/migrations/), applied in order at
start-up: [`0001_init.sql`](../backend/migrations/0001_init.sql) and the ten after it (`0002_guards`,
`0003_operator_import`, `0004_publisher_profile`, `0005_github_roles`, `0006_licensing`,
`0007_file_types`, `0008_conversions`, `0009_instance_connections`, `0010_storage_plugins`,
`0011_notifications`). In short:

| Table | Holds | Notes |
|---|---|---|
| `accounts` | a person | linked identities, never merged by e-mail; `operator` marks the one account the operator commands act as (no identity, nobody signs in as it; `0003`) |
| `identities` | a GitHub identity | unique `(provider, issuer, subject)`; subject = GitHub's numeric id. Rows of the removed Keycloak sign-in (`provider = 'oidc'`) stay as history; their `roles` column is unused since `0005` |
| `sessions` | server-side sessions | the cookie holds a random id, the table its sha256; CSRF token; the GitHub token sealed (AEAD) for the submission-time check; `roles` and `role_source` (`0005`): what the sign-in read from the administration repository, fixed for the session's life |
| `publishers`, `publisher_members` | who stands behind apps | bound to the GitHub owner's **numeric** id; `pending` until an administrator activates it; `verified`, `official`; `description` and `avatar_sha256` (`0004`): the GitHub owner's profile text and mirrored avatar, read when the publisher becomes active |
| `apps` | one catalog entry | `name` = the manifest's name (globally unique); one app per repository, bound to GitHub's numeric **repository id**; `listed` / `unlisted` / `revoked`; the price (`pricing_model`, `price_text`, `purchase_url`, `0006`); `kind` `app`, `language_pack` or `storage` (`0010`) |
| `app_versions` | an approved version | immutable except the yank fields and never deleted (a trigger enforces it); commit, every hash, the artifact key's signatures, filex's own permission rows, languages, engines; a storage plugin's `binaries` (every build by platform, pinned and signed) and `conformance` (the plugin validator's run) (`0010`) |
| `app_version_file_types` | the kinds of file a version works with, read from its manifest (`0007`) | derived, one row per version: written at approval, extracted again at start-up for older versions and older extractors ([File-type search](#file-type-search)) |
| `app_version_conversions` | what a version converts, as its listing declares it (`0008`) | a record, at most one row per version: written at approval from the reviewed listing, or set by the operator on an official publisher's version (audited); not in the signed index ([File-type search](#file-type-search)) |
| `submissions`, `submission_checks` | the pipeline | one open submission per repository and tag; checks per attempt; `operator_listing` holds an operator import's listing (`0003`), NULL for every web submission; `kind` `app` or `storage`: what was submitted (`0010`) |
| `reviews` | reviewer decisions and comments | |
| `media`, `app_media` | mirrored images | content-addressed (sha256); PNG, JPEG, WebP only |
| `signing_keys`, `demo_key_material` | public keys and their status; the throw-away seeds of `FAPPS_DEMO_KEYS=1` (the e2e stack, local development; the retired demo) | one `active` key per use: `index`, `artifact`, `license` (`0006`) |
| `licenses`, `license_activations` | license keys (as an HMAC and a prefix) and the instances using them (`0006`) | [Licensing](#licensing-paid-apps) |
| `install_intents` | install links (the token's sha256; the sealed license key until used or expired) (`0006`); `source` (`web` or `instance`) and `requested_by_instance` (`0009`) | [Install links](#install-links-magic-link) |
| `saved_instances` | a person's filex addresses, sealed, with a keyed index (`0006`); a connected filex's public key, its fingerprint and the filex's own id (`0009`) | owner only |
| `instance_connect_codes`, `instance_request_nonces` | connection codes (sha256 only, 30 minutes, one use) and the nonces of a connected filex's signed requests (kept 10 minutes) (`0009`) | [Instance connections](#instance-connections) |
| `index_releases` | every signed index ever served | append-only (a trigger refuses UPDATE and DELETE); `serial` only grows; `body` = the exact signed bytes |
| `jobs` | the work queue | `FOR UPDATE SKIP LOCKED`; `notify_event` and `notify_deliver` from `0011` |
| `notification_webhooks`, `push_subscriptions`, `notification_preferences` | a person's webhooks (format, events, the address, secret and header sealed, the last delivery), browser subscriptions (sealed, found by a keyed hash) and the events their browsers show (`0011`) | owner only ([Notifications](#notifications)) |
| `notification_events`, `notification_deliveries`, `notification_state` | every event raised (audience, a `dedupe_key` that makes it happen once), one row per channel it went to (status, attempts, HTTP status), the newest filex release announced (`0011`) | |
| `audit_log` | who did what | append-only (a trigger refuses UPDATE and DELETE), hash chain |
| `categories`, `reserved_names` | fixed lists | seeded |

The database enforces what the code promises never to do, in
[`0002_guards.sql`](../backend/migrations/0002_guards.sql) (migrations are applied once, each in one
transaction, by `internal/db`; an applied file that later changes is refused):

- `app_versions`: a `DELETE` is refused, and an `UPDATE` is refused unless it changes only
  `yanked_reason`, `yanked_at` and `yanked_by`. An approved version is immutable.
- `index_releases`: append-only, no `UPDATE`, no `DELETE`.
- `audit_log` (from `0001_init.sql`): no `UPDATE`, no `DELETE`.
- an index on the jobs' `submission_id`, so a withdrawal finds and cancels a submission's open jobs.

[`0003_operator_import.sql`](../backend/migrations/0003_operator_import.sql) adds
`submissions.operator_listing` and `accounts.operator` (at most one such account, a unique partial
index) for the [operator import](#operator-import-official-publisher).

Invariants the code keeps (and tests):

- A version is written only by an approval, in the same transaction as its audit row.
- An index release is written only by the index publisher, under a transaction-level advisory lock,
  with `serial = max + 1`; the bytes are built from the committed state and stored before they are
  served.
- `app_versions.manifest_sha256` / `wasm_sha256` / `ui_sha256` are the values the resolver measured
  AND every validator reported; a disagreement never reaches review.

## Identity and access

### Sign-in: GitHub, for everyone

- One sign-in for publishers, reviewers and administrators alike: a **GitHub OAuth App** (not a
  GitHub App: nothing acts on repositories), authorization code flow with `state` bound to a
  short-lived signed cookie. **No scopes**: public profile and public repositories are all the store
  reads with the user's token. (An organization's ownership proof would need `read:org`:
  [Q5](#open-questions).) Roles come from elsewhere: [Roles](#roles-the-administration-repository).
- The identity is GitHub's numeric user id; the login is display only.
- **Proof that the repository may be listed**, checked at every submission:
  1. the submitter is a member of the publisher in the store;
  2. GitHub says the submitter has `admin` or `maintain` on the repository (the repository object's
     `permissions`, read with the submitter's own token);
  3. the repository's owner (numeric id) is the publisher's bound GitHub owner;
  4. the repository holds **`.filex-apps.json`** at the submitted tag's commit, naming the publisher
     ([schema](../schema/filex-apps.schema.json)). It is the owner's written consent, and it pins the
     listing's metadata (categories, summary, icon, screenshots, description, conversions) to the
     reviewed commit.
     For the official publisher only, an operator import stands in for this file
     ([Operator import](#operator-import-official-publisher)).

### Roles: the administration repository

The store grants no role itself. The reviewer and administrator roles come from the operator, in
one of two ways or both:

- **A list**: `FAPPS_ADMINS` and `FAPPS_REVIEWERS`, numeric GitHub user ids (`12345`, or
  `login:12345` with the login as a label). A bare login is refused at start-up: a login can be
  renamed and taken by another account, the numeric id cannot.
- **The administration repository**, `FAPPS_ADMIN_REPO` (`owner/name`, private; no default): the
  account's permission on it, as below. Its collaborators are the store's staff.

An account named in more than one place gets the highest of its roles. The rest of this section is
the repository's way.

| Role on the administration repository (GitHub's `role_name`) | Store role |
|---|---|
| `admin`, `maintain`, `write` (push) | **admin** (an administrator also holds every reviewer right) |
| `triage` | **reviewer** |
| `read`, not a collaborator (`none`), an account GitHub does not know | none (a publisher or a visitor) |

- **Read at every sign-in, kept for the session's life.** The session row keeps the roles and their
  source (`sessions.roles`, `role_source`, migration `0005`) and nothing re-reads them while it lasts.
  A permission taken away ends the role at the next sign-in, and at the latest when the session
  ends: a session with a role lasts **12 hours**, one without 7 days. A role granted later reaches
  only a new sign-in (another session of the same account does not pick it up).
- **The server asks, with its own token.** The repository is private, and the signing-in person's
  OAuth token has no scope: it reads public data only, so it cannot see the repository, and the
  repository object's `permissions` field (what the submission check reads for public
  repositories) is out of reach. The server calls GitHub's "Get repository permissions for a user",
  `GET /repos/{owner}/{repo}/collaborators/{login}/permission`, with **`FAPPS_ADMIN_TOKEN`**: a
  fine-grained token owned by the repository's owner, with repository access to the administration
  repository only and the single permission **Metadata: read**.
- **Measured on 2026-10-04** (on the reference store's private administration repository): a
  collaborator answers `{"permission": "admin", "role_name": "admin", "user": {"id": ..., "permissions":
  {"admin": true, "maintain": true, "push": true, "triage": true, "pull": true}, "role_name": "admin"}}`;
  an account that is not a collaborator `{"permission": "none", "role_name": "", "user": {... every
  permission false}}`; a login that is no account `404 "<login> is not a user"`. The legacy
  `permission` folds `maintain` into `write` and `triage` into `read`, so the rule reads `role_name`
  first; a custom role is judged by the `user.permissions` flags it carries (push or above: admin,
  triage: reviewer), and an answer without `role_name` falls back to the legacy value (where `read`
  stays no role: closed). The answer's `user.id` must be the signing-in account's id. Then once more
  **with a real `FAPPS_ADMIN_TOKEN`** (fine-grained, that repository only, Metadata: read), after the
  repository was added to it: `GET /repos/{owner}/{repo}` 200; an administrator 200 `admin` /
  `admin`; `octocat` 200 `none` / `""` (not a 404); a login that is no account 404; the same endpoint
  on a repository outside the token's grant (a public one) 403 "Resource not accessible by personal
  access token". **Metadata: read is enough.** While the token had no repository access, the
  repository and the permission both answered 404. An organization may also refuse a fine-grained
  token that lives longer than its limit (403 "... forbids access via a fine-grained personal access
  tokens if the token's lifetime is greater than 366 days").
- **It fails closed.** Each of these signs the person in **without a store role** (the sign-in
  itself succeeds; a publisher's work does not depend on the role check), writes a log line and shows
  in the check's health:

  | Case | Health status | Log |
  |---|---|---|
  | `FAPPS_ADMIN_TOKEN` not set | `unconfigured` | warning at start-up and at each sign-in |
  | the token does not see the repository: the permission call AND `GET /repos/{owner}/{repo}` answer 404 (a fine-grained token without the repository or waiting for the organization's approval, a wrong `FAPPS_ADMIN_REPO`, revoked access: GitHub answers the same 404 for all of them), or 403 "Resource not accessible by personal access token" | `repo_unreachable` | error |
  | the owner's policy refuses the token: 403 "... forbids access via a fine-grained personal access tokens ..." (a lifetime over the organization's limit) or SAML enforcement | `repo_unreachable` | error |
  | GitHub refuses the token (401), limits it (403/429), fails (5xx) or does not answer | `failing` | error |
  | an answer without a role, or naming another account | unchanged | error |

  A 404 for the permission while the repository answers 200 is a login GitHub does not know: no
  role, and nothing is wrong with the check.
- **Where the health shows**: `/readyz` (`warnings`; the store stays ready), `GET
  /api/v1/admin/health` (administrators; asks GitHub again on every call) and the start-up log (the
  server asks once at start, a few times more while GitHub fails). An unapproved token therefore reads
  as "the administration repository is not visible to FAPPS_ADMIN_TOKEN", never as a quiet "nobody
  is an administrator".
- **Who, which role, why**: every sign-in writes `auth.signin` to the audit log with the actor role
  (`admin`, `reviewer` or `publisher`) and `data` = `{provider, login, roles, role_source}`;
  `role_source` says where the role came from; for the repository it reads
  `github repo permission <owner>/<name>: write` (or `...: not checked (<why>)`). `GET /api/v1/me` answers the same `role_source`. The later decisions of that session (an
  approval, a yank) carry the session's role as their actor role ([Q9](#open-questions)).

### Sessions

- Cookie `fapps_session`: HttpOnly, Secure, SameSite=Lax, path `/`. Server-side row with an
  **absolute lifetime only**: 7 days, 12 hours for a session with a store role (privileged sessions
  are short). There is **no separate idle timeout** (a phase 1 decision; activity does not extend a
  session).
- Signing out deletes the row. Suspending a publisher deletes its members' sessions; disabling an
  account does too. Migration `0005` deleted the sessions of the removed Keycloak sign-in, and a
  session whose identity is not GitHub's signs nobody in.
- Without `FAPPS_SESSION_KEY` (outside development) the sign-in is closed and the store is
  read-only: the catalog and the signed index still work.
- `GET /api/v1/me` tells the web interface who is signed in **and whether the sign-in can be
  started**: `providers: {github}` (a boolean), in the signed-in `200` body and in the signed-out
  `401` body. The sign-in page reads it instead of probing `/auth/github/login`. The sign-in rate
  limit (10/min/IP) counts only real starts of `/auth/github/login`; `/api/v1/me` falls under the
  general API limit (120/min/IP) only. `/auth/oidc/*` (the removed Keycloak sign-in) answers 404.
- One identity per GitHub account, never linked to another by matching e-mail.

### Who may do what

| Action | Anonymous | Publisher member | Reviewer | Admin |
|---|---|---|---|---|
| Read the catalog, the index, media | yes | yes | yes | yes |
| Ask for a publisher | | any signed-in account | any signed-in account | any signed-in account |
| Submit, withdraw, retry | | for its publishers | | |
| See a submission | | its publishers' | all | all |
| Approve, reject, comment, re-run dry runs | | | yes | yes |
| See every app in any status (`GET /api/v1/admin/apps`: listed, unlisted, revoked) | | | yes | yes |
| Yank / unyank, revoke / unrevoke, list / unlist | | | | yes |
| Activate, suspend, verify publishers | | | | yes |
| Re-sign the index, see keys, validators, audit log | | | | yes |

Everyone signs in with GitHub, so a reviewer or an administrator is the same account as their
publisher memberships, and the columns add up. A reviewer may not approve a submission of a
publisher they are a member of (four-eyes), except an administrator in phase 1 while the owner is the
only reviewer ([Q9](#open-questions)): that approval is flagged `self_review` in the audit log, next
to the actor role the session's sign-in established.

## Submission pipeline

```
 POST /api/v1/submissions   (synchronous: membership, repository permission, rate limit)
        |
     queued --> resolving --> validating --> needs_review --> approved  (version + signed index)
        |           |              |              |      \--> rejected
        |           v              v              |
        |         failed  <--------+              +--> (recheck: back to validating)
        +--> withdrawn  (any time before a decision)
```

Every step writes a `submission_checks` row (`passed`, `warned`, `failed`, `skipped`) that the
publisher and the reviewer see, with a machine code and a sentence.

| # | Check | What | Fails with |
|---|---|---|---|
| 1 | `repo_access` | repository public, not archived; submitter has `admin`/`maintain` (a response without GitHub's `permissions` field is refused: fail closed); owner id = publisher's. **Runs synchronously in `POST /submissions`** with the submitter's own token (the backend compares the owner id); the background `Resolve` repeats the reads with an empty user token. GitHub's three answers are kept apart: a 404 is `repo_private` (private or gone), a refused token is `github_token_invalid` (the submitter's sign-in; their repository may well be public - BRF-Tech/filex#118) or `github_unavailable` (the store's own token, after the anonymous retry), and an address that is not `owner/name` is `repo_address` | `repo_private`, `repo_address`, `repo_permission`, `owner_mismatch`, `github_token_invalid`, `github_unavailable` |
| 2 | `tag` | `ref` is a **tag** (branches are refused: they move); resolved to its commit (annotated tags peeled); GitHub's tag signature verification recorded (informational) | `not_a_tag`, `tag_not_found` |
| 3 | `manifest` | `filex-app.json` read **at the commit** (not the tag name), sha256 of the bytes, parsed leniently for name, version, kind, range, permissions, artifact addresses | `manifest_missing`, `manifest_unreadable`, `too_large` |
| 4 | `consent` | `.filex-apps.json` at the commit, valid against the schema, names this publisher; every engine its `conversions` need is one the manifest asks for | `consent_missing`, `consent_publisher`, `consent_invalid` |
| 5 | `policy` | **written by the backend against the database** (`internal/pipeline/policy.go`); the resolver does not produce it. Name: a new app must not take an existing or reserved name, a known app must keep its name, its repository id and its kind (`kind_changed`: an app, a language pack and a storage plugin never turn into one another; the approval and `UpdateAppListing` refuse it again); a name one edit away from a listed app is `warned`; version: valid semver, not already listed, higher than the newest unless marked a backport (`warned`); license: the one GitHub detects **at the tag's commit** (`GET /repos/{o}/{r}/license?ref=<commit>`), read right after the tag is resolved; when GitHub answers NOASSERTION there, the default branch's verdict is carried over only if the license file's blob at the commit is identical to the one that verdict judged (at most two extra calls, noted in the check data); none means `license_missing` | `name_taken`, `name_reserved`, `name_changed`, `repo_changed`, `publisher_changed`, `kind_changed`, `version_invalid`, `version_exists`, `license_missing` |
| 6 | `artifacts` | module (`wasm.url`, `{tag}` expanded) and interface bundle (`ui.bundle.url`) downloaded through the guarded client, streamed to sha256 with size caps; the hashes must equal the manifest's | `sha256_mismatch`, `too_large`, `fetch_failed`, `host_not_allowed` |
| 7 | `media` | icon and screenshots named by the consent file, read at the commit, sniffed (PNG/JPEG/WebP), dimensions checked, stored by sha256 | `media_invalid`, `too_large` |
| 8 | `dry_run` | every validator whose filex version the manifest's range admits is asked `POST /api/admin/app-plugins?dry_run=1` with `{"github_repo","ref"}`; one row per validator, the answer kept per validator | see below |

**The dry run** (the validator lane; filex's contract: `docs/APP-PLUGINS-API.md`, "POST
/api/admin/app-plugins - install", in the filex repository):

- The store signs in to each validator (`POST /api/auth/login`, the seeded validator account) and
  calls the dry run with a Bearer token; an API key could only leave install requests, but the dry
  run itself is open to it too.
- **The range picks the validators here, before any call** (`internal/filexrange`, filex's own range
  grammar and version order), not filex's answer: a filex outside a range does not always say so
  (filex-convert 0.2.0, `>=0.50.0`, answers `400 manifest_invalid` on 0.49). The two validators are
  the newest filex (`latest`, today 0.53.0) and the oldest filex the store supports (`floor`, policy:
  the previous minor, today 0.52.0).
  A validator outside the range gets a `skipped` row with code
  `out_of_range`; a range that admits neither fails `no_validator_in_range`; a range that does not
  parse fails `range_invalid`. The version of each validator is read from `GET /api/capabilities`.
  The range written into the index is the **canonical** form (`filexrange.Canonical`: a repeated
  comparator or alternative is written once, so `filex` + `min_filex` giving `>=0.50.0, >=0.50.0`
  becomes `>=0.50.0`).
- **Passed** means: HTTP 200, `compat.ok` true (or no range), `kind` as resolved, and every hash filex
  reports equal to the resolver's: `manifest_sha256` (filex reports it only for an app without a
  module), `wasm_sha256` (apps with a module) and `ui_sha256` (interface bundles; the request carries
  `ExpectUISHA256`). The **permission comparison** takes filex's permission rows, **drops the ones filex
  derives itself** (`ui`, `ui:*`, `ui-net:*`, `ui-viewer:*`, `ui-new:*`, `thumbnail:*`, and from
  filex 0.56 `files:e2e-plaintext` and `files:co-edit`, from the `encrypted_folders` and `co_edit`
  blocks, and from filex 0.57 `office-engine`, from the `engines.office` block:
  `resolve.IsDerivedPermission`) and compares what is left with the manifest's declared set
  (`permissions_mismatch`). Without that, every interface app would fail on permissions its manifest
  never wrote. filex's own refusals (`manifest_invalid`, `incompatible`, `sha256_mismatch`, ...) are
  kept verbatim with their code.
- The permission rows (filex's own words for each permission; the wording differs between filex
  versions, so they are kept **per validator**), `languages` (a language pack's coverage, which also
  differs by validator: 100 % on 0.50, 99 % on 0.49 for the Spanish pack), `engines_missing` (engine
  ids) and `file_types` are stored with the version: the app page shows exactly what filex's install
  review will show. The index's `versions[].languages` come from the first validator in the
  configured order (the newest).

**Mechanics.** Jobs in Postgres (`jobs`), claimed with `FOR UPDATE SKIP LOCKED`, at most one dry run
per validator at a time (a large module compiles for tens of seconds in filex), per-job time limit
(resolve 5 minutes, dry run 4 minutes per validator), retries with backoff for network errors only. A
check that fails is final for that attempt; **retry** starts a new attempt (the tag is resolved again,
so a moved tag is caught). The GitHub token of the submitter is used only in step 1 (synchronously,
in `POST /submissions`); background work uses `FAPPS_GITHUB_TOKEN` if set, else anonymous reads (raw
and release downloads are not API calls; the API reads cost three calls per resolution, so anonymous
reads allow about 20 resolutions an hour: set the token).

### Storage plugins

From migration `0010` the store lists filex's other plugin class (filex 0.55): a
**storage plugin** - a program filex runs as itself, outside any sandbox, handed the credentials of
every storage made on it (filex `docs/PLUGINS.md`). A publisher submits it with `kind: storage`
(`POST /api/v1/submissions`, the submit page's **What you submit**; the operator import's
`-kind storage`). The same submission, the same states, the same review; what the steps read differs:

| # | Check | For a storage plugin |
|---|---|---|
| 1, 2 | `repo_access`, `tag` | as for an app |
| 3 | `manifest` | the **feed**: `filex-storage.json` attached to the tag's release (`releases/download/<tag>/filex-storage.json`, through the guarded client), sha256 of the bytes, parsed as filex parses it (`plugin.parseFeed`): name (filex's plugin-name rule), version (semver), `filex` range, `notes` (clipped to 8 KiB), `binaries` by `os/arch` with an https URL and a lower-case sha256 each. **`linux/amd64` and `linux/arm64` are required** (`platform_missing`); a platform outside linux, windows and darwin on amd64 and arm64 is refused (`platform_unknown`) |
| 4 | `consent` | `.filex-apps.json` at the commit, as for an app; a storage plugin declares no `conversions` |
| 5 | `policy` | as for an app (name, version, license) |
| 6 | `artifacts` | **every build** the feed names downloaded and held to the feed's sha256 (`sha256_mismatch`), capped by `FAPPS_MAX_BINARY_MB` (256), and each one an **executable of its own platform's format, for its platform's architecture** - a 64-bit ELF program for `linux/*` (machine `x86-64` or `aarch64`, and **statically linked**: an ELF naming a program interpreter, `PT_INTERP`, is refused), a 64-bit PE program (not a DLL) for `windows/*`, a Mach-O program (thin, or universal with a slice for the architecture) for `darwin/*` - read from its first 64 KiB while it is hashed, with the `debug/elf`, `debug/pe` and `debug/macho` types (`build_not_native`). The store lists **self-contained builds**: a script that needs an interpreter is refused, even though filex's image has a shell that would run a `#!/bin/sh` build on the validator, because a filex on another host has no promise of the interpreter |
| 7 | `media` | as for an app |
| 8 | `dry_run` | on a **plugin validator** instead of the filex validators (below) |

**The plugin validator.** `filex plugin-validator --spool DIR` (filex 0.55 and later, filex
`docs/CLI.md`) runs in a container with `network_mode: none`, a read-only root, a tmpfs for the runs,
`cap_drop: ALL` plus what it needs to start the plugin as another user, and a memory, CPU and process
ceiling. The store and the validator share one volume (`internal/plugincheck`):

| File | Written by | Holds |
|---|---|---|
| `status.json` | the validator, every 5 seconds | protocol, filex version, platform, the job it runs. A heartbeat older than 90 s is a validator that is not there (retried, never a verdict) |
| `in/<id>.bin` | the store | the build for the validator's platform, downloaded again through the guarded client and held to the feed's sha256 before it is written (`.partial`, then renamed) |
| `in/<id>.json` | the store, last | `{protocol, id, sha256, name, version, created_at}` |
| `out/<id>.json` | the validator | `ok`, a `code` and `message` when not, the sha256 it measured, the plugin's `describe`, filex's conformance report |

The validator copies the build into a private directory while it hashes it, refuses one that is not
the pinned sha256, starts it as an unprivileged user of its own (never root, never the validator's or the store's user; the run's directory and the build stay root's, every process of that user is killed between two runs - filex `docs/PLUGINS.md`), and runs **filex's own** checks
(`plugin.CheckBinary`: the handshake, the describe filex accepts, every conformance probe of the
capabilities the plugin declared, against its `/v1/selftest` area). `in/` belongs to the store's user,
`out/` to the validator, so the plugin under test can neither leave a job nor forge a result.

**Passed** means: an answer for the same sha256, `ok`, a describe whose `name` and `version` are the
feed's (`name_mismatch`, `version_mismatch`), a verified conformance report (`conformance_unverified`: no selftest, so
nothing was probed) with no failure (`conformance_failed`). The range picks the plugin validator as
it picks filex validators (`out_of_range`, `no_validator_in_range`); a validator whose platform the
feed has no build for fails `no_build_for_platform`. filex's own refusals (`start_failed`,
`handshake`, `describe_invalid`, `no_selftest`, ...) are kept verbatim. Only the validator's platform
is run (today `linux/amd64`); the other builds are pinned and signed but not executed, and the app
page says so.

**Approval** signs every build with the artifact key into `app_versions.binaries` - not its
sha256 alone, as an app's module is, but the **build's text**: the plugin's name, the version, the
platform and the sha256 ([Signatures and keys](#signatures-and-keys)), keeps the run's summary as `conformance` (platform, filex version, passed,
failed, skipped, driver, the capabilities it proved), and the version enters the index with
`binaries` and `conformance` beside `manifest` (the feed); `wasm` and `ui` are null, `permissions`
empty. Both fields are omitted for an app, so an app's index entry keeps its bytes; a filex before
0.55 skips an entry of `kind: storage`. An **install intent** for a storage plugin carries the feed's
address (`feed_url`, its sha256 as `manifest_sha256`), every build (`binaries`) and `conformance`;
filex re-reads the feed, holds this server's build to the store's pin, checks the store's signature
- as the link's plugin and version on its own platform - against `FILEX_PLUGIN_TRUSTED_KEYS`, and
installs it as its **From its source** would. A paid storage
plugin is sold as an app is ([Licensing](#licensing-paid-apps)); filex keeps its license under a row of
its own.

## Review and policy

The reviewer sees: the checks, the dry-run answers side by side, the manifest, the permission rows,
the diff against the newest approved version (permissions added and removed, range, module or
interface changed), the consent metadata and screenshots, links to the tag and its compare view on
GitHub.

Policy (from the earlier plan, A2.6; `docs/POLICY.md` in phase 2):

- Accepted: open source under an OSI license, public repository, tagged release, permission reasons
  in every declared language (English at least), no trademark misuse, no obfuscated code, any live
  address an interface loads (`ui.external`) justified.
- Checked by the pipeline: everything in the table above. Checked by the reviewer: the license is
  real (not only detected), the code matches the description, permissions are proportionate, no
  obfuscation, reproducible-build claims (phase 2 badge: the store rebuilds from the tag and compares).
- Refused or withdrawn: harmful behaviour, false statements, license violations.

**Approval** (one transaction): the version row with the artifact key's signatures over its hashes;
the kinds of file it works with, read from its manifest ([File-type search](#file-type-search));
on a first approval the app row; the listing from the consent file (categories, summary, icon,
screenshots, description); the review and audit rows. Then the index publisher builds and signs a new
release. The answer names the new `serial`.

## Operator import (official publisher)

The official publisher's tags (filex-sign, filex-convert, filextext, filex-drawio, the language
packs) were cut before the store existed and carry no `.filex-apps.json`; adding it would mean a new
release of every app. So the store has one more door, for an **official, verified** publisher only,
and only from inside the store's own container: operator commands of the server binary
(`backend/cmd/server/operator.go`). The pipeline behind them is the one every submission goes
through.

```sh
docker compose exec app /fapps-server publisher -id brf-tech -name "BRF Tech" \
  -github-owner BRF-Tech -homepage https://github.com/BRF-Tech
docker compose exec app /fapps-server import -publisher brf-tech -repo BRF-Tech/filex-sign \
  -ref v0.3.0 -listing /tmp/fapps-seed-filex-sign/listing.json -approve
```

**`publisher`** reads the GitHub owner by login (`GET /users/{login}`, the server's token), creates
the publisher bound to its **numeric** id when it does not exist (the operator account is its owner
member) and makes it active, verified and official. A publisher already bound to another owner is
refused (`owner_mismatch`). Audited as `publisher.request` (with `via: operator`) and
`publisher.update`.

**`import`**, in order:

1. The **listing file** has the `.filex-apps.json` schema and goes through the same parser
   (`internal/consent`): categories, summary, description, icon, screenshots. Its paths are files next
   to it (the schema's path rule keeps them inside that folder: no absolute path, no `..`), under the
   same size caps as files read from a repository.
2. **Synchronous checks** (a refusal queues nothing): the publisher is active, **official and
   verified** (`publisher_not_official`); the listing names it (`consent_publisher`); the tag is not
   listed yet and not in the pipeline; the repository is public and not archived, and its owner's
   numeric id, as GitHub answers now, is the publisher's bound owner id (`owner_mismatch`). The
   `repo_access` row records `not asked: operator import (official publisher, consent by ownership)`
   where a web submission records the submitter's role.
3. The submission is **queued like any other**, submitted by the operator account
   (`accounts.operator`), with the listing kept on it (`submissions.operator_listing`, the images
   inline) so that whichever worker resolves it reads the same listing. The server's workers run the
   whole pipeline: tag, manifest at the commit, consent (next point), policy, artifacts (downloaded,
   sha256 against the manifest), media (the operator's images through the same image rules and the
   same `MediaSink`, recorded with the address `operator:<path>`), the dry run on every validator, the
   pins.
4. **The consent check** does not read `.filex-apps.json`. It passes, with the sentence `operator
   import (official publisher, consent by ownership)` and the listing's file name and sha256 in its
   data, only when the publisher, **read from the database at that resolve**, is official and verified
   and the owner id **measured in that same resolution** is the publisher's bound owner id; otherwise
   it fails with `publisher_not_official`, `owner_mismatch` or `consent_publisher`. Every other check is
   unchanged.
5. The command waits for the workers and prints every check. With `-approve`, a submission that
   reached review is approved by the operator account acting as an administrator: Q9's exception (an
   administrator approving their own publisher), flagged in the audit row (`self_review: true`) next to
   `operator_import: true` and the consent sentence. Approval checks official and verified once more,
   signs the version with the artifact key and publishes the index, as for any approval. A tag that
   fails a check is not listed; the command exits with the failing check's code and message.

**The security boundary.** Running the commands needs a shell on the store's container, whose
environment already holds the database URL and the signing keys: that is full control of the store
anyway, so the door adds no new way in. It cannot list a repository whose owner is not the
publisher's bound owner, nor anything for a publisher an administrator has not made official and
verified, and it skips none of the checks that look at the code (manifest at the commit, artifacts by
sha256, every validator's dry run, the pins). What it replaces is only the repository owner's
written consent, which for the official publisher is the store owner's own decision. The web
interface never sets an operator listing (`Submit` has none), so a web submission still needs
`.filex-apps.json`, also for the official publisher
(`TestOperatorImport_TheWebPathStillNeedsTheConsentFile`).

**`conversions`** sets what an approved version declares it converts
([File-type search](#file-type-search)) from a listing file's `conversions`, for a version that was
listed before its listing declared them (filex-convert 0.2.0 was imported on 2026-10-04, the
declaration came on 2026-10-05; a tag is listed once, so a second import of it is refused with
`version_exists`):

```sh
docker compose exec app /fapps-server conversions -app convert -version 0.2.0 \
  -listing /tmp/fapps-seed-filex-convert/listing.json
```

It reads the file alone through the same parser (the schema), and in one transaction checks that the
app's publisher is official and verified (`publisher_not_official`), that the listing names it
(`consent_publisher`), that the version exists, and that every engine a conversion needs is one the
version's manifest asks for (`consent_invalid`); then it replaces the version's declaration
(`app_version_conversions`, `source` `operator`, `origin` the file's name and sha256), or removes it
when the file declares none. The same declaration again changes nothing and writes no audit row. The
signed index does not carry conversions, so nothing is published: the serial and the signature stay.

**`publish-index`** re-signs the index from the committed state (serial + 1, a new expiry) through
the same service call as the administration page's **Publish now** (`PublishNow`, reason `refresh`),
as the operator account:

```sh
docker compose exec app /fapps-server publish-index
# index serial 27 published: sha256 ..., key index-2026-10, expires ...
```

It is for a release that changes what the index says about versions already listed (as when a
version's permissions became filex's review list): the index is built at each release, so without a
release the new build is not served until the next approval or the refresh before expiry.

**Audit.** `submission.create` with `operator_import`, the consent sentence and the listing's name and
sha256; `submission.approve` with `operator_import`, `self_review` and the consent sentence;
`version.conversions` (target `<app>@<version>`) with the listing's name and sha256, the entries and
pairs, and the sha256 of the declaration before and after; `index.publish` (target the serial) with
the index's sha256. The operator account's display name is
`Operator (command line)`.

**Seed.** The official listings live in [`seed/official/<repository>/`](../seed/official/)
(`listing.json`, `description.md`, `icon.png` with its `icon.svg` source, `screenshots/`), checked by
`TestSeedListingsAreValid`; [`seed/official/import.sh`](../seed/official/import.sh) copies one folder
into the container and imports the tags given to it. How to run it: [DEPLOY.md](DEPLOY.md), "Official
apps".

## The signed index

`GET /v1/index.json` and `GET /v1/index.json.sig`. Schema 1 (`backend/internal/index/schema.go`,
`api/openapi.yaml` `Index`), following the earlier plan's K5 with these additions:

- `keys[].status` (`active` / `next` / `retired`) for rotation;
- `versions[].commit` (the tag's commit at approval) and `versions[].manifest.sig`;
- `icon` and `screenshots` point at this store's `/v1/media/<sha256>.<ext>`, never at GitHub;
- `revoked` / `yanked` are always present (`null` when not), so a client never confuses "absent" with
  "not revoked";
- `versions[].permissions` is **filex's review list**: the ids of the rows the validator's dry run
  answered, the same rows the app's page shows (`app_versions.permission_rows`), each once, in their
  order - the manifest's declared permissions AND the ones filex derives from its `ui`, `thumbnails`,
  `encrypted_folders` and `co_edit` blocks (`ui:eval`, `ui-viewer:.docx`, `files:e2e-plaintext`,
  `files:co-edit`, ...). A version kept without rows lists its declared permissions; a storage plugin
  lists none. A filex shows this list in its store before anything is installed - the count, the
  request a person sends, the administrator's first look at it - so it has to be the list the install
  review will show (the office editor 0.3.1 was once listed with 2 permissions where its review had
  21). The install intent keeps the declared list (`permissions` there is held to the manifest).

Rules:

- **Canonical bytes**: object keys sorted, two-space indent, no HTML escaping, UTF-8, a trailing
  newline. The same catalog state gives the same bytes (tested).
- **`serial`** grows by one at every release; a filex keeps the highest it accepted and refuses a
  lower one (rollback defence).
- **`expires_at`** = `generated_at` + 60 days (`FAPPS_INDEX_TTL_DAYS`); a filex refuses an expired
  index (freeze defence). A daily job re-signs the same content (serial + 1) once fewer than 30 days
  are left (`FAPPS_INDEX_REFRESH_DAYS`). A store that was stopped catches up at start.
- **Releases** happen on: approve, yank, unyank, revoke, unrevoke, list/unlist, a publisher change
  that the index shows, refresh, key rotation. Each is a row in `index_releases` with its reason.
- **Serving**: the latest row's exact `body`; `ETag` = its sha256; `X-Index-Serial`;
  `Cache-Control: public, max-age=300`. The `.sig` is served from the same row. Before the first
  release both answer `503 index_not_ready` (never an empty document a client could cache).
- Unsigned extras (phase 2): `/v1/releases` history for audit and mirrors.

**How a filex verifies** (for the later client work, mirroring the plan's A2.9): fetch both files;
`plugin.VerifyDetached(trustedIndexKeys, sha256hex(body), sig)`; refuse if `expires_at` has passed or
`serial` is below the stored maximum; store the new maximum. The trusted index key is built into or
configured on the filex (`FILEX_APP_STORE_KEYS`), never taken from the index itself.

## Signatures and keys

- **Scheme** (one rule with filex, `backend/internal/plugin/signature.go` in the filex repository):
  ed25519 over the **lower-hex sha256 text** of the signed bytes; signatures written as lower-case hex
  (filex also reads base64). `internal/signing` implements it; its test vectors were produced by Node's
  crypto independently of the Go code, and the same vectors pass filex's own `VerifyDetached`
  ([Testing](#testing)).
- **Three keys**: the **index key** signs index bodies (and install intents); the **license key**
  signs license verdicts and is listed in `/v1/keys.json` only (not in the index, so adding it changed
  no index); the **artifact key** signs each version's
  `wasm.sha256`, `ui.sha256` and `manifest.sha256`, and every storage plugin build (below). A filex
  that lists the artifact key in `FILEX_PLUGIN_TRUSTED_KEYS` accepts store-signed modules. One key
  leaking does not open the other's door.
- **A storage plugin build** is signed over its own text, not its sha256 (filex 0.55,
  `signing.StorageBuildPayload`; filex `plugin.BuildClaim.Payload` / `VerifyBuild`): ed25519 over the
  bytes of

  ```text
  filex-storage-build:v1
  <name>
  <version>
  <platform>
  <sha256>
  ```

  the name trimmed, the version without a leading `v`, the platform (`linux/amd64`) and the sha256
  lower case, joined by single line feeds with none at the end, signed as they are (not hashed
  again). A signature over the sha256 alone vouched for the bytes only, so a filex trusting the
  artifact key took any build the store ever signed - an old, yanked version, another plugin's,
  another platform's - as "signed" on every install path; the text names what the store reviewed,
  and starts with its own name so it can never be read as a sha256 or as any other text the store
  signs. filex rebuilds it from the install link it is installing, and a build the store's artifact
  key signed that arrives any other way is taken only while this store's index still lists that
  version (filex remembers a trusted store's `artifact` keys from `keys.json`). One vector pins the
  text in both programs: `TestStorageBuild_TheSharedVector` here, `TestVerifyBuild_TheSharedVector`
  in filex. `fapps sign -key <seed> -sha256 <sha> -name <n> -version <v> -platform <os/arch>`
  signs a build by hand.
- **A separate key for storage builds** is possible without a filex change: filex 0.55 treats
  every `artifact` key a trusted store publishes as a build key. It would be published in
  `/v1/keys.json` as a second `artifact` key with its own id (never a new `use` - a filex before 0.55
  refuses a `keys.json` with a use it does not know), and the store would need a key slot for it
  (`signing_keys` allows one active key per use today). Not configured: the artifact key signs
  storage builds, and the build text keeps those signatures apart from its module signatures. (filex today passes no signature on a GitHub install; carrying `wasm.sig` through the install
  is a small filex change for the client phase.)
- **Rotation** (the design, **phase 2**): a new key is published with `status: next` in at least one
  release before it signs; then it becomes `active` and the old one `retired` (kept in `keys` for one
  TTL). Index keys are also built into filex clients, so an index key rotation needs a filex release
  first. **Phase 1 behaves more bluntly:** at start-up a new key from the environment (a different key
  id for a use) makes the previous active key `retired` at once, with no `next` step, and a retired key
  is refused if it comes back. Rotate only when nothing needs the old key to verify.
- **Custody**: private keys only in the environment. With `FAPPS_DEMO_KEYS=1` (refused unless
  `APP_ENV` is `demo` or `development`: the e2e stack and local development; the retired demo ran
  with it) the store generates throw-away keys and keeps their seeds in `demo_key_material`; such keys
  are marked `demo` and are never trusted by a real filex. A store's own keys come from
  `fapps-server keygen` ([SELF-HOSTING.md](SELF-HOSTING.md#make-the-stores-keys)). The reference
  store's custody is [Q3](#open-questions): its keys (`index-2026-10`, `artifact-2026-10`,
  `license-2026-10`) were generated on its host, live only in its environment and in the owner's
  offline backups; their public halves and fingerprints are in
  [`keys/production.json`](../keys/production.json), signed with the release key that signs filex's
  tags ([SELF-HOSTING.md](SELF-HOSTING.md#publish-your-keys-fingerprints)).

## Media mirror

Icons and screenshots are read from the repository at the reviewed commit, never hot-linked: a
listing cannot change after review, and viewers' browsers do not talk to GitHub. PNG, JPEG and WebP
only (no SVG: it can carry script); at most 5 MiB each, 8 screenshots, dimensions within bounds,
decoded to check the declared type. Stored in Postgres in phase 1 (backed up with the database),
served at `/v1/media/<sha256>.<ext>` with `Cache-Control: public, max-age=31536000, immutable` and
`Content-Security-Policy: default-src 'none'`. Without an icon the page draws the manifest's `icon`
name with filex's icon set.

A **publisher's avatar** is mirrored the same way: when a publisher becomes active (an administrator's
activation or the operator's `publisher` command), the store reads the GitHub owner (`GET
/users/{login}`, checked against the bound numeric id), keeps its profile text (`bio`, an
organization's description too) as the publisher's `description` and mirrors its avatar from
`avatars.githubusercontent.com` (asked at 256 px; square PNG, JPEG or WebP, 32 to 1024 px, fully
decoded) into `media`. Best effort: GitHub failing leaves the publisher active without a profile. The
operator's `publisher` command reads it again on demand. Its address (`avatar_url`) comes with the
publisher in the publishers' list and page and, since 2026-10-05, with an app's publisher in the
catalog, on the app's page and in Administration > Apps (the same query as the app, no extra
request); the app page's publisher card draws it, or the initial when there is none.

Artifacts (modules, bundles) are NOT mirrored in phase 1: the index points at GitHub release assets,
pinned by sha256 and signed. A content-addressed artifact mirror (`/v1/a/<sha256>`) for air-gapped
installs is phase 3 ([Q8](#open-questions)).

## Yank and revoke

| | Yank (a version) | Revoke (an app) |
|---|---|---|
| Meaning | do not install this version; installed copies should move on | harmful: installed copies should be removed |
| Index | stays, `yanked: {reason, at}`; `latest` skips it | stays, `revoked: {reason, at}`; all versions stay listed for identification |
| Catalog | shown with a warning, no install instructions | app page says why; absent from lists |
| Who | admin | admin |
| Reversible | unyank | unrevoke |

Both publish a new index at once and are written to the audit log with the reason.

## What a filex reads

The **client contract** is the `/v1/` paths, public and stable within `schema: 1`:

| Path | For |
|---|---|
| `/v1/index.json` + `/v1/index.json.sig` | the catalog, verified |
| `/v1/media/<sha256>.<ext>` | icons and screenshots for the Store tab |
| `/v1/keys.json` | informational; a filex trusts the key it was built or configured with (the `license` key is listed here, not in the index) |
| `POST /v1/licenses/verify` | a license key's check for one app on one instance, signed with the license key ([Licensing](#licensing-paid-apps)) |
| `GET /v1/install/{token}`, `POST /v1/install/{token}/complete` | an install link's intent, signed with the index key, and its outcome ([Install links](#install-links-magic-link)) |
| `POST /v1/instances/connect`, `GET`/`DELETE /v1/instances/{id}/connection`, `POST /v1/instances/{id}/intents` | a connected filex: its key bound with a one-time code (the answer signed with the index key), then intents asked for in its own name with signed requests ([Instance connections](#instance-connections)) |

The filex client (later, in the filex repository; earlier plan A2.9): settings `FILEX_APP_STORE`
(on/off), `FILEX_APP_STORE_URL` (default this store's `/v1/index.json`), `FILEX_APP_STORE_KEYS`; the
Store tab uses filex's DataTable; **Install** goes through filex's existing GitHub install with the
index's `repo` and `ref`, compares the dry run's `manifest_sha256` / `wasm_sha256` with the index, and
passes `wasm.sig` as the signature; update notices compare installed versions with `latest` within the
range; `yanked` / `revoked` show a warning strip. The `/api/v1/` paths are for the store's own web
interface; a filex does not depend on them. The public publisher list (`GET /api/v1/publishers`: active publishers
only, each with its `description`, mirrored `avatar_url` and the number of its listed apps; search `q`, sort
`name` or `apps`, paging) needs no sign-in. The public list (`GET /api/v1/apps`) holds listed apps only; the
administrators' and reviewers' list, `GET /api/v1/admin/apps` (`status` = `listed`, `unlisted`, `revoked` or
`all`, search, sort, paging; each row is an app summary plus `status` and, when revoked, `revoked_at`), is what
Administration > Apps shows, in one `DataTable` with a status filter.

## Licensing (paid apps)

The owner, 2026-10-04: paid apps get license keys the store generates and keeps, and the store is
the channel that checks them. **No payment** in this phase: the store shows a price and where to buy,
and a publisher sends the buyer a key made here. (Taking payments is a later decision.)

- **Price** (`apps.pricing_model`, `price_text`, `purchase_url`; migration `0006`): `free` or `paid`,
  for display only. A paid app may show a price text (at most 80 characters, e.g. "$29 per instance")
  and an https purchase address; a free app has neither (the database refuses it). Every app summary
  carries `pricing`; the catalog card and the app page show a "Paid" chip with the price text and a
  "Buy a license" link. The signed index does not carry the price (a filex learns `paid` from an
  install intent).
- **Who manages it**: a member of the app's publisher (the publisher active) or an administrator:
  the price (`PUT /api/v1/apps/{name}/pricing`), the licenses (`POST`/`GET
  /api/v1/apps/{name}/licenses`, `POST /api/v1/licenses/{id}/revoke`), the activations (`GET
  .../activations`, `POST .../activations/{instance_id}/release`). Reviewers have no right here. The
  web interface: Publish > Your apps > Price and licenses (`/publish/apps/<name>`), the same page from
  the app page and from Administration > Apps.
- **The key**: `FXL-` and 26 Crockford base32 characters (130 random bits) in groups of four, the last
  group two (`FXL-7K2Q-9WXM-3HDF-0PZA-QRST-V8MN-4C`). Reading a key forgives case, spaces, missing
  dashes and the look-alikes (I and L as 1, O as 0). The key is in the creating answer **once**; the
  table keeps `key_hash` = HMAC-SHA256 under HKDF(`FAPPS_DATA_KEY`, "license-key") and `key_prefix`
  (the first 8 characters, to tell keys apart). Neither the database, the logs nor the audit log ever
  holds a key (tested: every table as text, every binary column's raw bytes, the server log).
- **A license** (`licenses`): app, licensee name, an optional contact (e-mail or GitHub login), seats
  (at least 1), `valid_until` and `updates_until` (dates, through the end of that UTC day; empty = no
  end), status `active` or `revoked` (revoking cannot be undone), a note, who made it and when.
- **Activations** (`license_activations`): one row per filex instance that checked the key in
  (`instance_id`, filex's opaque id: printable ASCII, no space, at most 128), first and last check,
  the filex version it reported, `released_at`. An activation that is not released holds a seat.
  Releasing frees the seat; the instance takes one again at its next check if one is free.

**The check** a filex server makes: `POST /v1/licenses/verify` with `{key, app, instance_id,
filex_version?}` (public, no session, no CSRF; 30 a minute per address and 60 an hour per key, the key
counted by a piece of its HMAC). A well-formed request always answers `200` with a verdict signed by
the store's **license key** (`SignedEnvelope`, below). In order:

| Result | When | What the payload carries |
|---|---|---|
| `invalid` | the key is malformed or unknown | nothing about any license |
| `wrong_app` | the key belongs to another app | nothing about the license |
| `revoked` | the license is revoked | licensee, seats, seats used, dates |
| `expired` | today (UTC) is past `valid_until` | the same |
| `seats_exhausted` | the instance holds no seat and none is free | the same |
| `valid` | otherwise; a new instance takes a seat, a known one updates its last check | the same |

Every verdict carries `app`, `instance_id`, `checked_at`, `next_check_by` (+24 hours) and
`grace_until` (+14 days: a filex that cannot reach the store keeps its last `valid` verdict until
then). Optional fields are absent, never `null`. The audit log records a new activation
(`license.activate`) and every refused check of a known key (`license.check_refused`), by license id
and prefix; an unknown key is only counted, never recorded.

## Install links (magic link)

The owner, 2026-10-04: an **Install** button makes a link to the person's own filex; opening it there
leads to filex's install review. This answers [Q1](#open-questions) as decided: the link pre-fills,
a filex administrator still reads the permissions and confirms.

```
 store page          the store                          the person's filex
 Install --------->  POST /api/v1/install-intents
 (signed in)         <- {token, magic_link, expires_at}
 opens magic_link ---------------------------------->   /admin/store-install#store=<store>&intent=<token>
                                                        (an admin session; the fragment never
                                                         reaches a server log or a Referer)
                     GET /v1/install/{token}  <-------  filex server: the signed intent
                     POST /v1/install/{token}/complete <- {instance_id, result}: the token is spent
```

- **The intent** (`POST /api/v1/install-intents`, a signed-in person, CSRF): `{app, version?,
  filex_url? | instance_id?, save_instance?, instance_label?, license_key?}`. Without `version` the
  newest version that is not yanked is taken: the store does not know the target filex's version (it
  never contacts it), so the intent carries `filex_range` and filex refuses a version outside it. A
  revoked app (`409 app_revoked`) or a yanked version (`409 version_yanked`) is refused. A
  `license_key` must be an active key of this app (`400 license_invalid` otherwise, one code for
  every reason). 30 intents an hour per account.
- **The token**: 32 random bytes, base64url (43 characters), stored only as its sha256, valid **30
  minutes**, spent by the first completion. Its short id (`token_id`, 16 hex, random, not derived from
  the token) names it in the payload and the audit log. The access log writes
  `/v1/install/{token}` without the token.
- **The link**: `<filex origin>/admin/store-install#store=<encodeURIComponent(store origin)>&intent=<token>`.
  `filex_url` is reduced to its origin: https only (with `APP_ENV=development`, `http://localhost`,
  `http://127.0.0.1` and `http://[::1]` too), no user, no path, no query, no fragment, the host
  lower-cased, the default port dropped (`400 filex_url_invalid` otherwise). The intent keeps that
  origin **sealed** (AES-256-GCM under HKDF(`FAPPS_DATA_KEY`, "install-intents"), the intent's id and the
  field as additional data, so it cannot be moved to another intent): it goes into the signed payload as
  `filex_origin`, never into a log or the audit log.
- **What filex reads**: `GET /v1/install/{token}` (public: the token is the secret; 60 a minute per
  address) answers the intent signed with the **index key**: `{store, filex_origin, token_id, app, kind,
  version, repo, ref, commit, manifest_sha256, wasm_sha256?, ui_sha256?, permissions, filex_range, paid,
  license_key?, expires_at}`, the approved version's own pins (the same as the index). `commit` (the
  tag's commit at approval) and `manifest_sha256` are always set (the database requires both for every
  version, and the store refuses to sign a row without them). **`filex_origin`** is the filex the intent
  was made for, the same value that starts the magic link (a saved instance's address when the intent
  named one): a filex refuses an intent whose `filex_origin` is not its own address, so a leaked link
  cannot be used on another filex (the security review, 2026-10-04). `410
  intent_used` / `410 intent_expired` once spent or expired, `404` for a token the store never made,
  `409 app_revoked` / `409 version_yanked` when that happened after the link was made.
  `POST /v1/install/{token}/complete` with `{instance_id, result: installed|cancelled}` spends the
  token (only the first call) and drops the sealed license key.
- **The license key on the way**: when the person adds one, it is sealed in `install_intents`
  (AES-256-GCM under HKDF(`FAPPS_DATA_KEY`, "install-intents"), the intent's id as additional data),
  read back only into the signed payload, and set to `NULL` when the intent is completed; a sealed key
  of an intent that expired unused is dropped at the next intent. A key is never stored in the clear.
- **Saved instances** (`GET`/`POST /api/v1/me/instances`, `DELETE /api/v1/me/instances/{id}`; the
  account menu's "My instances", `/me/instances`): a person's filex addresses, **encrypted** and
  readable by their owner only. The address and the label are sealed (AES-256-GCM under
  HKDF(`FAPPS_DATA_KEY`, "instances"), the row's id and the field as additional data, so a value
  cannot be moved to another row); `url_index` = HMAC-SHA256 under HKDF(`FAPPS_DATA_KEY`,
  "instances-index") of the account id and the origin finds a second save of the same address
  without reading any address (the same address of two accounts gives two unrelated values). At most
  50 per account. An instance of another account answers `404 instance_not_found`; the audit log
  records `instance.save` / `instance.delete` by id only.
- **The filex side** (the page `/admin/store-install`, reading and completing the intent, the license
  check) ships in **filex 0.52.0** (the owner's decision, 2026-10-04; on 2026-10-05 the owner made
  0.52.0 the first release that carries it, with no patch release before it); the dialog says so, and
  an older filex installs by hand.
- **The web interface**: the app page's **Install** button opens a dialog (saved instance or address,
  "save this instance", a license key field on a paid app); signed out, it goes to the sign-in and
  comes back with `?install=1`, which opens the dialog. The link is shown with Copy and "Open in my
  filex" and the note that it works once and for 30 minutes and must be opened by a filex
  administrator. The manual steps (repository and tag) stay below as "Install by hand".

### Instance connections

The store side of filex's embedded store screen (filex 0.53): a filex SERVER asks for an
install intent in its own name. In filex, a person asks for an app from the store's catalog; when a
filex administrator approves the request, filex asks the store for a FRESH intent (a stored one would
have expired: the request may wait days) and opens it in its own install review, exactly as a magic link
would. The store does not learn who asked inside filex.

```
 store page                       the store                         the filex (its administrator)
 My instances > Connect --------> POST /api/v1/me/instances/{id}/connect-code
 (the instance's owner)           <- {code: fxc_..., expires_at}     pastes the code (Apps > Trusted stores
                                                                      > Connect); filex makes an ed25519
                                  POST /v1/instances/connect <------ key for this store and sends
                                  {code, public_key, filex_origin,    the public half
                                   filex_instance_id?}
                                  -> signed (index key): {store, instance_id, filex_origin,
                                     key_fingerprint, connected_at} -> filex checks the signature, its
                                                                       own address and key, keeps it
 later, on an approval:           POST /v1/instances/{id}/intents <- a signed request {app, version?,
                                  -> {token, token_id, ...}            license_key?}
                                  GET /v1/install/{token} <--------- read and reviewed like any link
```

- **The code** (`POST /api/v1/me/instances/{id}/connect-code`, the owner, CSRF): `fxc_` and 32 random
  bytes in base64url, in that answer once; the table keeps its sha256. 30 minutes, one use, at most 5
  waiting per instance, 10 an hour per account. The page shows it with Copy and what it is for; a row of
  a connected instance offers "Connect again" (the new key replaces the old) and "Disconnect".
- **The connection** (`POST /v1/instances/connect`, public, 10 a minute per address): the code, the
  filex's public key (32 bytes, lower-case hex), its own address (`filex_origin`, normalized like an
  install link's) and its opaque id (`filex_instance_id`, what it counts license seats by). The address
  must be the one the instance was saved with: otherwise `409 wrong_instance` and NOTHING is bound (a
  code pasted into the wrong filex leaves no key behind). A key another instance holds is
  `409 key_in_use`. The answer is a `SignedEnvelope` with the index key over `{store, instance_id,
  filex_origin, key_fingerprint, connected_at}`: the filex verifies it with the index key it already
  trusts (no new key, no new use) and refuses an address or a fingerprint that is not its own. The audit
  log records `instance.connect_code` and `instance.connect` (by the key's fingerprint).
- **A signed request**: five headers, `Fapps-Instance` (the instance id), `Fapps-Key` (the key's
  fingerprint, sha256 of its raw bytes), `Fapps-Timestamp` (Unix seconds), `Fapps-Nonce` (16-64 of
  `A-Z a-z 0-9 _ -`, new each time), `Fapps-Signature` (lower-hex ed25519). The signed text is eight
  lines joined by a newline: `FAPPS-INSTANCE-REQUEST-1`, the method in upper case, the path as sent
  without a query, the instance id, the fingerprint, the timestamp, the nonce and the lower-hex sha256 of
  the body; the signature is over the text's lower-hex sha256, the store's one signature rule
  ([Signatures and keys](#signatures-and-keys)). Test vector made with Node's crypto:
  `backend/internal/install/testdata/request-vector.json` (filex's tests read the same vector).
- **What the store holds a request to**: 60 signed requests a minute per address, counted before any
  database work; then the timestamp within 5 minutes of the store's clock (`401 request_expired`), the
  instance, its key and the signature over everything above (`401 instance_unauthorized`, one answer for
  every reason), the path's instance the same as the header's, and the nonce not seen from this
  instance in the last 10 minutes, spent only after the signature holds (`401 request_replayed`).
  The key opens `/v1/instances/{its id}/...` and nothing else: not another instance, not the store's own
  `/api/` paths.
- **The intent** (`POST /v1/instances/{id}/intents`, 30 an hour per instance): `{app, version?,
  license_key?}`, the same checks as a person's (`app_revoked`, `version_yanked`, ...). Its account is
  the instance's owner, its `filex_origin` the instance's saved address (so the filex that asked is the
  only one that takes it), `source = 'instance'` and `requested_by_instance` record where it came from.
  The answer is `{token, token_id, expires_at, app, version}`; the filex reads the signed intent at
  `GET /v1/install/{token}` and completes it like any other. A license key goes in only when the filex's
  administrator gave one at the approval, and only for a license with a seat for that filex: one its
  `filex_instance_id` holds, or a free one (`400 license_invalid` otherwise) - a key is not a way to move
  another instance's seat.
- **Letting go**: the filex's own signed `DELETE /v1/instances/{id}/connection`, or the owner's
  `DELETE /api/v1/me/instances/{id}/connection`; forgetting the instance drops it too. The filex's
  signed `GET /v1/instances/{id}/connection` answers what the store holds (id, address, fingerprint,
  since when).
- **Why a key and not a shared secret**: the store keeps only public keys, so nothing read from its
  database or backups signs as a filex; the nonce and the timestamp make a captured request useless
  after the fact; the code proves nothing on its own (whoever spends it first binds a key, and only for
  the instance's own address, where intents are what anybody signed in could make anyway).

### The signed envelope

The license verdict and the install intent are `{"payload": {...}, "key_id": "<id>", "signature":
"<hex>"}` (`domain.SignedEnvelope`, `internal/signedjson`). The signature is the key's ed25519 over the
**lower-hex sha256 of the payload's canonical JSON**, the scheme the index uses
([Signatures and keys](#signatures-and-keys)). Canonical JSON here is **compact**: object keys sorted
by their UTF-8 bytes, no insignificant whitespace, **no trailing newline**, UTF-8, `<` `>` `&` written as
themselves (Go's `encoding/json` with `SetEscapeHTML(false)`, the encoder's newline trimmed). (The
index's own bytes are indented with two spaces and end with a newline; a filex verifies those bytes as
served, while an envelope's payload is re-canonicalized.) The store refuses control characters and
U+2028 / U+2029 in anything that reaches a payload, so JavaScript's `JSON.stringify` of the key-sorted
tree gives the same bytes. A verifier re-canonicalizes the payload **as a generic JSON tree** (a
struct would reorder or drop keys). Checked independently: vectors made by Node's own JSON and crypto
(`internal/signedjson/testdata`), a hand-written verifier in the integration tests, and the e2e run's
Node verifier against the live answers.

## File-type search

The owner, 2026-10-05: a search for a kind of file (`docx`, `.docx`, `*.docx`, `pdf docx`) answers
with what filex does with it by itself and with the apps that work with it, each saying what it
does with it.

- **What an app works with** comes from its approved manifest, read the way filex reads the same
  fields (`internal/filetypes`, `Extract`):

  | Place in `filex-app.json` | Role | Notes |
  |---|---|---|
  | a `views[]` entry with `placement: viewer` | `open` | the app's interface opens the file in place of filex's preview |
  | a `new_documents[]` row | `create` | filex's New menu makes a file of that kind |
  | the `thumbnails` block | `thumbnail` | |
  | an `actions[]` entry | `action` | the action's own label; a `hidden` action is not listed (no menu offers it); `applies.engine_ext` adds kinds only while filex has that engine (`engine`, e.g. `office`); an output name that fixes an extension (`{stem}.pdf`) is its `output_ext` |
  | a `views[]` entry with `placement: inspector` | `inspect` | a section of the details panel |

  A rule's kinds are `applies.ext` (trimmed, one leading dot dropped, lower case: filex's
  `validateApplies`; filex's extension grammar) and `applies.mime` (exact or a family like
  `image/*`). A rule that names no kind on files is offered **on every file** (`any_file`); a folder
  rule (`kind: dir`) names no file type. A media-type rule matches an extension through filex's own
  table of kinds (`assoc.knownKinds`, carried in `filex-builtin.json`), as filex itself turns an
  app's media types into kinds. A language pack names nothing.
- **What an app converts** comes from its **listing**, not its manifest: an action offered on every
  file (filex-convert's Convert…) names no kind, and which kinds it turns into which is its module's
  answer when it runs. The owner, 2026-10-05: the publisher declares it in `.filex-apps.json`
  (`conversions`, [schema](../schema/filex-apps.schema.json)), and it is reviewed with the code:

  ```json
  "conversions": [
    {"from": ["docx", "odt"], "to": ["pdf", "txt"]},
    {"from": ["doc"], "to": ["pdf"], "needs": ["office"]}
  ]
  ```

  Each entry: every `from` extension converts into every `to` extension (no extension in both),
  needing every engine of `needs` on the filex it runs in; without `needs`, nothing else. A pair
  that works with any one of several engines is written once per engine. The consent check holds
  it to the schema (filex's extension grammar, 1 to 256 entries, 1 to 128 extensions a side, 1 to 6
  engines, at most 16384 from-to pairs in all; `internal/consent`) and to the manifest: every engine
  a conversion needs must be one the manifest asks for (an `engines:<id>` permission or its
  `engines` list; `office` is the ONLYOFFICE Document Server connected to filex), else
  `consent_invalid`. The check's data says how many entries and pairs; the submission page shows
  every entry under "Listing", for the reviewer to compare with the code. It becomes the role
  **`convert`** of the search: an app whose conversions start from `.docx` **names** `.docx` (it
  counts in `named` and comes with the apps for the kind; its every-file action stays on its card,
  apart), each `convert` role with its targets (`to`) and its condition (`needs_any`: any one of
  these engine sets; a target that also works with nothing is said only there), the one that needs
  nothing first. The search matches `from` only (a search for `.pdf` is for apps that work with
  PDFs, not for every app that writes one), and a bare word a conversion starts from reads as an
  extension. A card sums it up ("Converts .docx → .pdf, .epub, .zip +7 more, and 19 more with an
  engine"); the app page's "Works with" shows a table of it (`conversions` in the version's answer,
  `filetypes.ConversionTable`: one row per kind, the kinds that convert alike on one row, the same
  roles the search gives for each).
- **Stored per version, derived**: `app_version_file_types` (migration `0007`): the rules, and every
  extension, media type and the any-file flag as searchable columns. A row is written in the
  approval's own transaction. The versions approved before `0007`, and every row an older extractor
  wrote (`filetypes.Extractor`), are extracted again from `app_versions.manifest` at start-up
  (`store.BackfillFileTypes`; a manifest that cannot be read is logged and tried again at the next
  start). `app_versions` stays immutable: nothing is written to it. The **conversions are a record**,
  not derived: `app_version_conversions` (migration `0008`), written in the approval's own
  transaction from the reviewed listing (`source` `listing`, `origin` the repository and commit or
  the operator's listing file and its sha256), or set afterwards on an official, verified
  publisher's version by the operator (`fapps-server conversions`, `source` `operator`, audited
  `version.conversions`; [Operator import](#operator-import-official-publisher)). No extractor reads
  them again; the search reads them beside the file types (`store.FileTypesOf`). They are **not in
  the signed index** (filex does not read them): setting them publishes nothing, and the index's
  serial and signature stay as they were.
- **Which version speaks for an app**: the one its card shows, the newest that is not yanked (with
  `filex=`, the newest whose range admits that filex). An older version's or a yanked version's
  kinds are not searched.
- **Reading the search** (`GET /api/v1/apps?q=`): it is a file-type search when every word is an
  extension. A word with a leading dot (or `*.`) always is one; a bare word only when filex knows it
  or a listed app's shown version names it (`docx` and `pdf docx` are file-type searches, `sign`
  stays a word, and so does `xyz`; `.xyz` is a search for that kind). Any other search is the text
  search, unchanged. The items are the apps that name one of the kinds (the more of them, the
  earlier; `sort` orders within that), then the apps that are there only for something they offer
  on every file; each item carries `matched_extensions`. The answer's `file_types` holds the kinds,
  how many items name one of them (`named`; the page counts the others apart: "No app works with
  .xyz; 1 offered on every file"),
  what filex does by itself with each (`builtin`, with the `engines` it needs) and up to 12 other
  listed apps whose name, label or summary holds an extension (`near`). The kind, category,
  publisher, verified and filex filters still apply. A version's answer (`GET
  /api/v1/apps/{name}/versions/{version}`) carries the same reading kind by kind as `file_types`:
  the app page's "Works with".
- **What filex does by itself** is
  [`backend/internal/filetypes/filex-builtin.json`](../backend/internal/filetypes/filex-builtin.json),
  generated from a filex tag (today `v0.56.0`) by
  [`scripts/gen-filex-builtin.mjs`](../scripts/gen-filex-builtin.mjs), never edited by hand. It reads
  the preview's viewers and its Edit button (`packages/core/src/modals/PreviewModal.vue`,
  `viewers/Viewer3D.vue`), the server's rule of how a file is edited (`backend/internal/editkind`,
  since 0.54: the office documents, and the text the built-in editor saves; checked against the copy
  filex's tests hold), the kinds ONLYOFFICE also opens (`backend/internal/assoc`), the
  thumbnail engine (`backend/internal/thumb`), the archive providers (`archivecli`), the New menu
  (`newdoc`), the settings (`config.go`) and the tools of the default image (`docker/Dockerfile`);
  the file's `sources` name each one with its lines. Each capability (`preview`, `play`, `browse`,
  `edit`, `thumbnail`, `create`) says what it `needs`: `office` (an ONLYOFFICE Document Server a
  filex administrator connects), `drawio` (a draw.io server), or a program on the server
  (`ffmpeg`, `ghostscript`, `imagemagick`, `sevenzip`, `sevenzip-rar`). The page says "out of the
  box" only when filex shows the file with nothing connected: `.docx` reads "filex opens it with
  ONLYOFFICE", with the setting, and "without it filex offers the file as a download". Not
  claimed, on purpose: a HEIC preview (filex hands it to an `<img>`, which only Safari decodes),
  the 3D formats the 3D viewer refuses, Edit for SVG and Mermaid (no save), Edit of a `.csv` or
  `.tsv` as text (its table only shows it; a `.csv` is edited in ONLYOFFICE), names with no
  extension (`Makefile`), a `.key` thumbnail (only a Keynote package). How to make it again: [DEPLOY.md](DEPLOY.md#filexs-own-file-types).
- **What the official apps name** (their approved manifests, measured 2026-10-05): filex-sign 0.3.0:
  `.pdf` (a Signatures details section; Sign…, Request signatures…, Sign / Fill, Verify; "Apply a
  signer's submission" is hidden); filex-drawio 31.5.3: `.drawio` (open, create) and `.dio` (open);
  filextext 0.1.1: `.fxtxt` (open, create); filex-retro 0.1.0: `.wad` and `.fxdoom` (the DOOM player),
  `.jsdos` (the DOS player); media-thumbs 0.1.0 (listed 2026-10-07): `.psd`, `.psb`, `.af`,
  `.afphoto`, `.afdesign`, `.afpub`, `.pxd`, `.ttf`, `.otf`, `.woff`, `.woff2` (opens them in its
  preview, draws their thumbnails); filex-convert 0.2.0: one action, Convert…, **on every file** (its
  manifest names no kind), and its listing declares 106 conversion entries (2720 from-to pairs of
  extensions; 2148 source format, target format and condition triples; 95 source extensions):
  [`seed/official/filex-convert/listing.json`](../seed/official/filex-convert/listing.json), written by
  [`scripts/gen-convert-conversions.sh`](../scripts/gen-convert-conversions.sh) from filex-convert's
  own planner at the tag (`graph.Route`, the call its wizard makes for the target list; routes of up
  to three steps; a pair is declared without `needs` only when pure Go in the sandbox does it, else
  once per smallest engine set), never by hand; the source note beside it
  ([`CONVERSIONS.md`](../seed/official/filex-convert/CONVERSIONS.md)) names the files and lines it rests
  on. The language packs: none.
- **The web interface draws, it does not match** ([below](#web-interface-and-reuse-of-filex-components)):
  `web/src/lib/fileTypes.ts` holds only the words for the server's answer.

## Notifications

A signed-in person hears what concerns them in the store (since 2026-10-20), on two kinds of
channel, both chosen on the page **Notifications** (`/me/notifications`, the header's bell):

- **webhooks**: an https address of theirs in one of four formats, chosen per webhook - **generic JSON**
  (signed), **Slack** (an incoming webhook's `text` + `blocks`), **Discord** (one embed, `allowed_mentions`
  empty so no name can ping `@everyone`), **Teams** (an Adaptive Card, as a Workflows "When a Teams webhook
  request is received" flow and the older incoming webhook take it). At most 10 per account.
- **browser notifications**: Web Push with VAPID. The page registers the store's service worker
  (`/sw.js`, which only shows a notification and opens the store's own page on a click), asks the
  browser for permission and hands the subscription to the store. At most 20 browsers per account,
  one event choice for all of them.

**Events** (`internal/notify`, `events.go`), and who hears them:

| Event (`kind`) | Raised when | Who |
|---|---|---|
| `submission.app_new` | a submission of a new app passes its checks and waits for review (once per submission and attempt: a reviewer's re-run of the dry runs does not raise it again) | administrators |
| `submission.version_new` | the same for a new version of a listed app | administrators |
| `publisher.requested` | someone asks for a publisher (it waits for an administrator to activate it) | administrators |
| `pipeline.error` | a submission fails because the store could not check it - `dry_run_error`, `validator_unavailable`, `resolve_error`, `resolver_unavailable` (the validators or GitHub did not answer, attempts used) - not because of the submission | administrators |
| `submission.approved` | a submission is approved | the publisher's members, never the one who decided |
| `submission.rejected` | a submission is not approved (the reviewer's reason in the message) | the same |
| `filex.released` | the store knows a newer filex release than before (below) | everyone who chose it |

An **operator import** raises no review or decision event: the operator is at the console that imports
and approves it. A person may choose only the events offered to them; the administrators' four are
offered to an administrator only.

**Administrators are asked again.** An administrators' event goes to an account only while GitHub
still says it is one: the fan-out reads the account's permission on the administration repository
with `FAPPS_ADMIN_TOKEN`, as the sign-in does (`notify.RepoAdmins`, answers kept 10 minutes). A
permission taken away ends the notifications within 10 minutes, without a sign-in. When GitHub does not
answer, the fan-out waits and asks again (10 s, 30 s, 90 s, 270 s) rather than guess, and in the end
leaves out the accounts it could not ask.

**`filex.released` comes from what the store already knows**, never from GitHub: the version of the
`filex-builtin.json` the image carries and the versions its validators and plugin validators report
(`GET /api/capabilities`, the spool heartbeat). A minute after start and every ten minutes the newest
**release** among them (a pre-release or a development build is none) is compared with the last one
announced (`notification_state` `filex_latest`): newer is announced once (`dedupe_key` `filex:<version>`),
the same or older is not. The first look on a store that never announced one only remembers the
version. A new release therefore reaches the store when the operator deploys an image or updates a
validator, and is announced within ten minutes.

**How an event travels.** The event is a `notification_events` row written in the same transaction as
the change it reports, with a `notify_event` job (`notify.Emit`). The job finds the channels that chose
the event (`notify_event` fan-out: webhooks by their `events`, browsers by the account's
`notification_preferences`), writes one `notification_deliveries` row per channel and one
`notify_deliver` job per row. A delivery is tried once and retried after 10, 30 and 90 seconds when
the receiver did not answer, timed out (10 seconds an attempt), or answered 408, 425, 429 or 5xx;
another answer is final. Each attempt's outcome is kept on the delivery and on the channel (`last_status`,
`last_http_status`, `last_error`, `last_at`): the page shows it. A push service that answers 404 or 410
means the browser's subscription is gone: the store forgets it. Every word of a message is written on
the server (`notify.Render`, English, as the store is).

**The generic format.** `POST` with `Content-Type: application/json`:

| Field | What |
|---|---|
| `id` | the delivery (the same on every retry: a receiver drops a repeat); `test-...` for a test |
| `event` | the kind, as in the table above, or `test` |
| `occurred_at` | when the event was raised, RFC 3339 |
| `source` | the store's host (`apps.filex.sh`) |
| `title`, `message` | the notification in words |
| `severity` | `info`, `success`, `warning` or `danger` |
| `action_url` | the store's page to act on (or filex's release notes for `filex.released`) |
| `data` | the event's facts (submission id, app, version, publisher, repository, tag, ...) |

Headers: `X-Fapps-Event`, `X-Fapps-Delivery`, `X-Fapps-Timestamp` (Unix seconds) and
`X-Fapps-Signature`: `sha256=` and the lower-hex HMAC-SHA256, keyed with the webhook's secret, of the
timestamp, a full stop and the body. The secret (`whsec_...`) is made by the store and shown once, when
the webhook is added. A generic webhook may also carry **one header of the owner's** (an API key; not
`Host`, `Content-Type`, `Cookie` or `X-Fapps-*`), kept encrypted and never shown again. Title, message,
severity and `action_url` read as most notification services' send call does, so such a service
can take the body as it is (a group in the address's query, its key in that header).

**What is kept, and how.** A webhook's address (a Slack or Discord address is itself the credential),
its signing secret and its header's value, and a browser's push endpoint and keys, are sealed with
AES-256-GCM under a key derived from `FAPPS_DATA_KEY` (`internal/datakey` "notifications"), each bound
to its row and field; a browser's endpoint is found again by a keyed hash. The list shows an address's
scheme and host only (`https://hooks.slack.com/…`), and no error message, log line or audit row carries
an address. Adding and deleting a webhook is audited (`webhook.create`, `webhook.delete`). Without
`FAPPS_DATA_KEY` the notification routes answer `503 data_key_missing`; without a VAPID key pair the
browser notifications are off (`503 push_unavailable`) and the webhooks work.

**Where the store may send.** Every address goes through the guarded client in its `AnyHost` mode: any
host and port, but HTTPS only, and after DNS only a public address - checked in the dialer on every
connection and every redirect (at most 3), so a name that resolves to loopback, a private, link-local,
CGNAT or metadata address, or a redirect to one, is never called (`internal/safehttp`). Adding a
webhook refuses an address that names or resolves to such an address at once (`address_not_public`).
`APP_ENV=development` alone takes plain http and, with `FAPPS_FETCH_ALLOW_PRIVATE=1`, a receiver on
the developer's machine.

**Operator commands**: `fapps-server notify-webhook` adds a webhook to an account by its GitHub login
(the header's value on standard input), `fapps-server notify-test` sends a test to each of an
account's webhooks ([DEPLOY.md](DEPLOY.md#notifications-operator-commands)).

## Security

| Threat | Defence |
|---|---|
| SSRF through a manifest's addresses | One guarded client (`internal/safehttp`): HTTPS only; host on the allow list (`github.com`, `api.github.com`, `raw.githubusercontent.com`, `objects.githubusercontent.com`, `release-assets.githubusercontent.com`, `github-releases.githubusercontent.com`, `codeload.github.com`, `avatars.githubusercontent.com` for publishers' avatars; configurable); after DNS the address must be public (no loopback, private, link-local, CGNAT, metadata), checked in the dialer for every connection; every redirect re-checked, at most 5; no `https` to `http` |
| Resource exhaustion | size caps (manifest 16 MiB, module 64 MiB, interface bundle 128 MiB, media 5 MiB; filex's own caps), streaming hashes (nothing large is held in memory), per-request and per-job timeouts, one dry run per validator at a time, job concurrency limits |
| Moved tag / swapped release asset | the commit is recorded; the manifest is read at the commit; every artifact is pinned by sha256 and must match in the resolver AND in every validator; retry resolves again; the index carries the hashes and signatures |
| Repository rename, transfer, re-registration | apps bound to GitHub's numeric repository id, publishers to the numeric owner id |
| Name squatting / confusables | global names, reserved list, one-edit-distance warning to the reviewer, first approval curated |
| Index rollback / freeze | `serial` monotonic, `expires_at`, refresh job |
| Key compromise | three keys, one per use (index, artifact, license), environment-only custody, rotation with `next` (phase 2; [phase 1 retires the old key at once](#signatures-and-keys)), demo keys never trusted |
| CSRF | SameSite=Lax cookie + per-session token in `X-CSRF-Token` on every state change + `Origin`/`Referer` must be the store; OAuth `state` cookies |
| A role that should not be there | roles only from the operator's list of numeric GitHub user ids or from GitHub's answer about the administration repository, read with the server's token at every sign-in, the answer's account id checked; every doubt fails closed (no role) and shows in `/readyz` and the admin health; a session keeps its roles and lasts 12 hours with one; the audit log records each sign-in's roles and their source |
| Session theft | HttpOnly, Secure cookie; only the id's sha256 stored; short privileged sessions; sign-out deletes |
| SSRF through a webhook or a push endpoint (an address a person typed or a browser handed over) | the guarded client without an allow list (`AnyHost`) but with every other rule: HTTPS, a public address after DNS on every connection and every redirect (at most 3); a webhook naming or resolving to a non-public address is refused when it is added; 10 seconds an attempt ([Notifications](#notifications)) |
| A webhook's address or key leaking | sealed (AES-256-GCM, per-row additional data) under a `FAPPS_DATA_KEY` derivation; the list shows scheme and host only; errors, logs and audit rows never carry an address; the signing secret shown once |
| A former administrator still hearing the review queue | the administrators' events ask GitHub again at delivery (answers kept 10 minutes); a check that does not answer delivers to nobody it could not ask |
| Abuse / brute force | rate limits in Redis: sign-in starts 10/min/IP, submissions 10/hour/account and 30/day/publisher, API 120/min/IP (`GET /api/v1/store` and its logo, fixed values every page load asks for, outside it), index uncapped but cached |
| XSS through listing content | text is text; the description's Markdown rendered and sanitized server-side (no script, style attributes, forms, frames) and sanitized again in the page (DOMPurify); release notes are only a link to the GitHub release; strict CSP (`script-src 'self'`, no inline script, `frame-ancestors 'none'`); media with `default-src 'none'`; no SVG |
| Malicious app attacking the validator | a dry run downloads and parses, it does not run the module; validators hold no data, no secrets beyond their throw-away admin, no ingress, memory limits |
| Secrets in the repository | none: secrets come from the environment; `.env.example` holds only non-secrets; gitleaks in the pre-push hook and in CI |
| Tampering with history | append-only audit log with a hash chain; every index release kept |
| A license key leaking from the store | only an HMAC (HKDF of `FAPPS_DATA_KEY`) and an 8-character prefix are stored; the key is in the creating answer once and never logged (tests read every table, every binary column's bytes and the server log) |
| Guessing license keys | 130 random bits; checks limited per address and per key; an unknown key learns nothing (`invalid`), another app's key only `wrong_app` |
| A forged or altered verdict or intent | signed with the license or the index key over canonical JSON; a filex verifies with the key it trusts |
| An install link reused, leaked into logs or stolen later | 32 random bytes, stored as sha256, 30 minutes, spent by the first completion; in the URL fragment (never sent to a server, never in a Referer); the access log drops it from the path; the signed intent names its filex (`filex_origin`), which another filex refuses |
| Saved addresses read from the database | sealed (AES-256-GCM, per-row additional data); duplicates found by a keyed hash bound to the account; only the owner reads them |
| A connected filex impersonated, or its requests replayed | the store holds the filex's public key only; every request signed over its method, path, instance, body, timestamp (5 minutes) and a single-use nonce; one answer for every failed check; codes 30 minutes, one use, sha256 only; the binding refuses an address that is not the instance's ([Instance connections](#instance-connections)) |

## Configuration

Read only in `backend/internal/config` (the list there is authoritative; this table mirrors it
variable for variable). A value that is present but malformed stops the server; what is missing is
reported by `/readyz` (the server still answers `/healthz`).

| Variable | Default | Secret | Meaning |
|---|---|---|---|
| `APP_URL`, `APP_ENV`, `DATABASE_URL`, `REDIS_URL` | `APP_ENV` = `development`; the others none | `DATABASE_URL` yes | from the environment (the operator's `.env`, the e2e stack's `e2e/stack.env`): origin, `production` / `development` (`demo`, the retired demo's, is still accepted), database (required to serve anything but `/healthz`), Redis (optional: an in-process limiter stands in) |
| `FAPPS_LISTEN` | `0.0.0.0:8080` | | bind address |
| `FAPPS_PUBLIC_URL` | `APP_URL` | | the origin written into absolute URLs (index media addresses, OAuth callbacks); required (this or `APP_URL`) |
| `FAPPS_TRUSTED_PROXIES` | empty | | comma list of addresses or CIDRs whose `X-Forwarded-For` is believed. Empty: only a loopback or private-address peer is trusted, and only for the right-most hop. Set: the header is walked from the right past every trusted hop. Feeds the rate limits and the audit log |
| `FAPPS_ANALYTICS_ORIGIN` | empty | | an optional page counter's origin, `https://host[:port]` and nothing else (e.g. `https://stats.example.com`); anything else stops the server. Set: the page's CSP adds it to `script-src` and `connect-src`, so a counter script the reverse proxy puts into the page loads and reports. The store prints no counter script itself. Empty: the CSP allows no other origin |
| `FAPPS_SESSION_KEY` | | **yes** | 32+ random bytes, base64; cookie signing and token sealing. Required outside `development`; without it the sign-in is closed |
| `FAPPS_GITHUB_CLIENT_ID` / `_SECRET` | | secret: yes | the GitHub OAuth App (publishers' sign-in) |
| `FAPPS_GITHUB_TOKEN` | | **yes** | optional read-only token for background GitHub reads (public read only, never a write scope) |
| `FAPPS_GITHUB_OAUTH_URL` | `https://github.com` | | GitHub's sign-in host; only tests and the e2e stack's mock change it |
| `FAPPS_GITHUB_API_URL` | `https://api.github.com` | | GitHub's REST API; same |
| `FAPPS_GITHUB_RAW_URL` | `https://raw.githubusercontent.com` | | where files are read at a commit; same |
| `FAPPS_STORE_NAME` | the host of `FAPPS_PUBLIC_URL` | | the store's name on its pages |
| `FAPPS_STORE_LOGO` | filex's mark | | a local SVG or PNG file (a path the server reads at start, 256 KiB at most) shown as the store's logo; served at `GET /api/v1/store/logo` under a sandbox CSP. The pages read the name, the logo's address and the footer's lines from `GET /api/v1/store`, which answers even before the database is ready |
| `FAPPS_OPERATOR_NAME`, `FAPPS_OPERATOR_URL` | none | | who runs the store and their address, in the footer |
| `FAPPS_CONTACT` | none | | how to reach the operator, `mailto:` or `https:` |
| `FAPPS_TERMS_URL`, `FAPPS_PRIVACY_URL` | none | | the operator's terms and privacy pages |
| `FAPPS_ADMINS`, `FAPPS_REVIEWERS` | none | | comma lists of numeric GitHub user ids, each `12345` or `login:12345`; a bare login stops the server ([Roles](#roles-the-administration-repository)) |
| `FAPPS_ADMIN_REPO` | none | | the administration repository, `owner/name` (a malformed value stops the server): `admin`, `maintain` or `write` on it make an administrator, `triage` a reviewer ([Roles](#roles-the-administration-repository)) |
| `FAPPS_ADMIN_TOKEN` | | **yes** | the role check's token: fine-grained, owned by the repository's owner, access to that repository only, Metadata: read; an organization may limit its lifetime. Without it the repository gives nobody a store role (`/readyz` warns) |
| `FAPPS_OIDC_*` (`_ISSUER`, `_CLIENT_ID`, `_CLIENT_SECRET`, `_REVIEWER_ROLE`, `_ADMIN_ROLE`) | | | **removed** with the Keycloak sign-in (2026-10-04): no longer read; one still set is logged at start-up and listed in `/readyz` `warnings`, never an error |
| `FAPPS_INDEX_KEY` + `_KEY_ID`, `FAPPS_ARTIFACT_KEY` + `_KEY_ID` | | **yes** | signing keys (seed or private key, hex or base64); required unless `FAPPS_DEMO_KEYS=1` |
| `FAPPS_LICENSE_KEY` + `_KEY_ID` | | **yes** | the license key (same format); set together or not at all. Without it (and without demo keys) license checks answer `503 license_key_missing` and `/readyz` warns |
| `FAPPS_DATA_KEY` | | **yes** | 32+ random bytes, base64: the license-key HMAC, the sealed license keys of install intents and the saved instances derive their keys from it (HKDF). Malformed stops the server. Without it (outside `development`, which makes a random one per process) licenses, install links and saved instances answer `503 data_key_missing` and `/readyz` warns. **Never change it on a store with data**: every license key would become unknown and every saved instance unreadable |
| `FAPPS_DEMO_KEYS` | `0` | | `1`: generate throw-away keys (index, artifact, license) when none are given; refused unless `APP_ENV` is `demo` or `development`. Set by `.env.example` for the e2e stack and local development; never in production |
| `FAPPS_INDEX_TTL_DAYS`, `FAPPS_INDEX_REFRESH_DAYS` | 60, 30 | | index lifetime; the index is re-signed once fewer than this many days are left. Positive integers |
| `FAPPS_VALIDATORS` | none (required) | | `label=url,...`, e.g. `latest=http://filex-latest:5212,floor=http://filex-floor:5212`; the order is the preference order |
| `FAPPS_VALIDATOR_EMAIL`, `FAPPS_VALIDATOR_PASSWORD` | `validator@local`, none | | the validators' throw-away admin |
| `FAPPS_FETCH_ALLOW_HOSTS` | the GitHub hosts in [Security](#security) | | the outbound allow list (comma list) |
| `FAPPS_MAX_MANIFEST_MB`, `_WASM_MB`, `_UI_MB`, `_MEDIA_MB` | 16, 64, 128, 5 | | size caps (positive integers) |
| `FAPPS_MAX_BINARY_MB` | 256 | | a storage plugin build's size cap (positive integer) |
| `FAPPS_PLUGIN_VALIDATORS` | none | | `label=/abs/spool,...`, e.g. `plugins=/spool/latest`: the plugin validators' spool folders, in preference order ([Storage plugins](#storage-plugins)). Empty: storage plugin submissions fail their dry run (`validator_unavailable`) and the log says so at start-up |
| `FAPPS_FETCH_CA_FILE` | none | | a PEM file the guarded client trusts in addition to the system roots. **Only with `APP_ENV=development`**: with any other `APP_ENV`, `config.Load` stops with an error |
| `FAPPS_FETCH_ALLOW_PRIVATE` | `0` | | `1` lets the guarded client dial private and loopback addresses (unspecified and multicast stay refused). **Only with `APP_ENV=development`**, like the CA file. HTTPS and the allow list stay in force |
| `FAPPS_VAPID_PUBLIC_KEY`, `FAPPS_VAPID_PRIVATE_KEY` | none | private: **yes** | the Web Push key pair of the browser notifications, base64url: the 65-byte uncompressed P-256 point and the 32-byte private scalar, set together or not at all (a pair that does not belong together stops the server). Without it the browser notifications are off and the webhooks work. A new pair makes every browser subscription stale (each browser turns notifications on again) |
| `FAPPS_VAPID_SUBJECT` | `FAPPS_CONTACT`, else `FAPPS_PUBLIC_URL` when it is https | | the push services' contact, `mailto:` or `https:` |

Not variables: the outbound timeout (60 s) and redirect limit (5) are fixed in `config.go`. Tests
alone read `FAPPS_TEST_DATABASE_URL` (`internal/db/dbtest`); the e2e stack's own settings are in
`e2e/stack.env`.

## Deployment

A store is one compose project: the store, its own PostgreSQL 16 and Redis 7, the two validators and
the plugin validator, behind an HTTPS reverse proxy, with an image tagged with its release, its own
keys (`fapps-server keygen`) and a database dump on a schedule. Setting one up:
[SELF-HOSTING.md](SELF-HOSTING.md) and [`deploy/compose/`](../deploy/compose/); keeping it running
(new versions, rollback, the validators at each filex release, the checks):
[DEPLOY.md](DEPLOY.md). The reference store, `https://apps.filex.sh` ([Q4](#open-questions)), runs
this shape since 2026-10-05 (announced with filex 0.52.0), with the official apps imported by the
operator import (index serial 14 on day one).

- The image is built from the [`Dockerfile`](../Dockerfile) (web build, Go build, distroless runtime
  with CA certificates, `HEALTHCHECK` through the binary itself): for a release tag by
  `.github/workflows/release.yml` (`ghcr.io/brf-tech/filex-apps:<version>`, amd64 and arm64), or by
  hand from a `git archive` of a commit.
- Migrations run at start-up (the server applies pending `migrations/*.sql` before serving `/api/`).
- [`deploy/compose/docker-compose.yml`](../deploy/compose/docker-compose.yml) is the production
  example. This repository's [`docker-compose.yml`](../docker-compose.yml) (`app`, `filex-latest`,
  `filex-floor`, `plugin-latest`; no database, port or network of its own) is the base of the
  end-to-end stack ([Testing](#testing)).
- A change is tried on the end-to-end stack first, then deployed
  ([DEPLOY.md, "Deploying a new version"](DEPLOY.md#deploying-a-new-version)).
- Resource budget: app 256 MiB, Postgres 384 MiB, Redis 64 MiB, each validator 768 MiB (limits in
  compose).

**The demo (2026-10-04 to 2026-10-05, retired)**: the first deployment was a demo with a shared
Postgres and Redis, throw-away keys (`FAPPS_DEMO_KEYS`, still used by the e2e stack and local
development) and its own GitHub OAuth App. It stayed as a staging copy for the day the reference
store went live and was **retired by the owner on 2026-10-05**; nothing of its data moved.

## Web interface and reuse of filex components

- Vue 3 + Vite + TypeScript, pnpm 9.12.0, the same tool versions as filex's web app (`vue` 3.5,
  `vite` 5.4, `vue-tsc` 2.1, `typescript` 5.6), `vue-router` 4, the icons of `lucide-vue-next` (the set
  `@brftech/filex-core` already ships). English only (an open-source surface), spelled as filex spells
  it ("license", never the British form; `web/tests/spelling.test.ts` checks `web/src`, these
  documents and the OpenAPI file). Every sentence lives in `web/src/strings.ts`, so a translation can follow.
- **Look**: filex's `--fe-*` tokens, light and dark; the store defines no colour of its own
  (`web/tests/guards.test.ts`). The header's **theme switch** is three icons in one strip, Light,
  System and Dark (`ThemeSwitch`, a `FaSegmented`): System follows the operating system
  (`prefers-color-scheme`), the choice is kept per browser (`localStorage` `fapps.theme`, guarded; a
  blocked storage simply follows the system) and every table follows it through `provideTableEnv`.
- **Pages**:
  - **Catalog** (`/`): a search field, category chips, and a toolbar with Kind, "Works with filex"
    (a version), Sort (name or recently updated), "Verified publishers only" and the view switch, Grid
    or List. The default is the **grid**: one card per app; the plain first page is arranged in
    shelves (from the filex team, community apps, language packs), and a search, a filter, another
    order or another page shows one grid of results. **List** (`?view=list`; only a view that is not
    the default rides in the address) is filex's `DataTable` (`store.catalog`), sorted and paged by the
    server. A publisher's page links to the catalog filtered by that publisher. The search field
    also takes a kind of file ("Search apps, or a file type like .docx"): when the server reads the
    search as file types ([File-type search](#file-type-search)), the page shows, for each kind, what
    filex does with it by itself (a panel; or "filex does not open .xyz by itself"), then "Apps that
    work with .docx" with what each app does with it on its card (".pdf: Sign, Verify, Signatures
    panel"), then "Offered on every file" apart, then other apps that mention the word; with neither
    filex nor an app for the kind, an empty state says so and links to Publish. The list view adds a
    "With .docx" column to the same `DataTable`.
  - **App page** (`/apps/<name>`, `/apps/<name>/versions/<version>`): **Install** (the dialog below)
    and "Install by hand" (the repository and the tag, each with a copy button), screenshots,
    "Works with" (the kinds of file the shown version names and what it does with each, each kind a
    link to the catalog's search for it), the permissions in filex's own words, the versions, the
    hashes and signatures, and the publisher's card with its mirrored logo (else its initial; the
    inline "by" line, here and on the cards, stays text: there the app's icon is the picture). A paid
    app shows a
    "Paid" chip with its price text and, when it has a purchase address, "Buy a license"; members of the app's publisher and
    administrators get **Price and licenses**; administrators also list/unlist, yank and revoke there.
  - **Install dialog**: one of the person's saved filex instances, or another address (with "Save
    this instance for next time" and a name), and on a paid app an optional license key; then the
    one-time link with Copy and "Open in my filex", and the note that it works once, for 30 minutes,
    opened by a filex administrator on filex 0.52.0 or later. Signed out, Install goes to the sign-in
    and comes back with `?install=1`, which opens the dialog.
  - **Price and licenses** (`/publish/apps/<name>`; reached from Publish > Your apps, the app page and
    Administration > Apps): the price (free or paid, a price text, a purchase address), the licenses
    in one `DataTable` (never a key), "New license…" (the key is shown once, in that dialog), revoke,
    and the filex instances that use a license, with "Free the seat".
  - **My filex instances** (`/me/instances`, the header's "My instances"): the saved addresses in one
    `DataTable`; save one, forget one.
  - **Notifications** (`/me/notifications`, the header's bell): this browser's notifications (turn on,
    which events, send a test, turn off) and the account's browsers in a `DataTable`; the webhooks in a
    `DataTable` with each one's format, address host, events and last delivery, and "Add a webhook..."
    (name, the format as a `FaSegmented`, the address, for the generic format one header, the events),
    a test, editing the name and events, deleting ([Notifications](#notifications)).
  - Also: the publishers (a gallery of cards with search and sort) and a publisher's page (its apps
    as cards); sign-in (one door, GitHub); Publish (the steps, your publishers, your submissions with
    a status filter, your apps); ask for a publisher; submit, with the listing helper that writes
    `.filex-apps.json`; a submission's page (checks, dry runs, permissions, reviews, the decision);
    the review queue; Administration (publishers, apps in every status, index and keys, validators,
    audit log).
- **Reuse, not imitation** (filex's rule "one table: the explorer's"): the published npm package
  `@brftech/filex-core` (pinned at 0.50.0) gives
  - `DataTable` (+ `DataColumn`, `provideTableEnv`): every table in the store is this component,
    never a `<table>` or a look-alike: 19 today, each under a `table-id` of its own that starts with
    `store.` (the guard test checks both). The catalog's list view is one of them; the catalog's grid
    and the publisher gallery are cards, not tables;
  - `style.css`: the `--fe-*` design tokens (light and dark) and the table's styles;
  - `LogoMark`, and `formatByteSize`, `formatWhen`, `formatWhenFull` for sizes and dates.
  An app's description is Markdown rendered and sanitized on the server (`internal/markdown`) and
  sanitized once more in the page (DOMPurify); release notes are a link to the GitHub release.
  Measured on the demo's build of `main` (2026-10-05): the entry script is 561 KB (179 KB gzip) and
  the stylesheet 309 KB (47 KB gzip); every page is a chunk of its own, loaded when it is opened.
  The optional viewer peers of the package (`monaco-editor`, `mermaid`, ...) are not needed and the
  build does not ask for them. If the size matters, the fix belongs in filex (a light `./table`
  sub-path export of `@brftech/filex-core`), not in a copy here.
- **No native dropdown** (the owner, 2026-10-04): a choice is `FaSelect` or `FaSegmented`, never a
  `<select>`; `web/tests/guards.test.ts` fails on any `<select` in a component or view.
  - `FaSelect` is a combobox with a listbox panel (the WAI-ARIA select-only combobox): keyboard
    (arrows, Home/End, Page Up/Down, typing jumps to a match, Escape closes without a change), the
    panel teleported to `<body>` or to the open `<dialog>` the combobox is in (a modal dialog is
    above everything else), opening upward near the window's bottom, and `required` kept for the
    form's own validation. Used for the catalog's Kind and Sort, Publish's submission status, the
    publisher on Submit and the saved instance in the Install dialog.
  - `FaSegmented` is two to four values side by side, a radio group (one tab stop, the arrow keys
    move the choice); with icons, the label is the accessible name and the tooltip. Used for the
    catalog's Grid / List, the publisher gallery's sort, the status filters of Administration's
    publishers and apps, and the theme switch.
  Both use only Vue, the icons they are given and filex's `--fe-*` variables, nothing of the store, so
  they can move into `@brftech/filex-core` as they are.
- Table preferences (column widths, sort) last for the page's session only, signed in or not: the
  store attaches no account store to filex's `tablePrefs`, so a reload forgets them.

## Testing

- **Go unit tests** for every package (state machine, canonical index bytes, range parsing, SSRF
  guard, consent schema, policy, CSRF, sessions). Network and GitHub are faked with `httptest`
  servers; the guarded client's allow list is injected in tests only.
- **Signature cross-check**: `internal/signing/testdata/vectors.json` (made by Node's crypto) is
  reproduced byte for byte by the Go signer (`signing_test.go`) AND verified by filex's own
  `VerifyDetached` ([`scripts/xcheck/run.sh`](../scripts/xcheck/run.sh): a throw-away module from
  filex's `signature.go`, in Docker; passed 2026-10-03 against filex `main` f6f8d6d2). A
  filex-side failure blocks any change to the signing code.
- **Integration tests** (`-tags integration`): the backend's against a real Postgres 16 (migrations,
  guards, queue, audit chain, index serials, the whole HTTP flow; [`scripts/integration.sh`](../scripts/integration.sh));
  the validator package's against both real filex validators (the image lines of
  [`scripts/xcheck/validators-it.sh`](../scripts/xcheck/validators-it.sh)) with **recorded real
  tags** (`testdata/fixtures.json`: filex-sign, filex-convert, a language pack; address, sha256 and
  size, the bytes cached outside the repository). The resolver reads GitHub's own names from a local TLS server (extra CA, private addresses,
  host map: the development-only modes), the validators read the same bytes from loopback
  (`FILEX_PLUGIN_LOOPBACK_SOURCES=1` on the test validators only).
- **Live run** (validator lane, by hand, never in CI): the production request body against the real
  GitHub (filex-drawio, filex-sign) on both validators. Kept apart so the e2e stack never needs the
  network beyond the image registries.
- **End-to-end** (Playwright, `mcr.microsoft.com/playwright` image, Chromium, Firefox and WebKit):
  the compose stack with a **mock GitHub OAuth + REST + raw + release host** (`e2e/mock-idp`,
  test-only; it also holds a private administration repository, `e2e-brf/filex-market`, that only
  the run's `FAPPS_ADMIN_TOKEN` sees), behind an HTTPS edge with a throw-away CA. The **real
  validators and the store use the production request bodies** and keep GitHub's own host names: those
  names resolve (`extra_hosts`) to a **sidecar** in each container's network namespace that serves the
  fixture repositories over HTTPS with the run's CA (GitHub tokens are HMAC-signed by the mock and
  accepted by the sidecar). The flows are clicked through the store's own pages: everyone signs in
  with GitHub (a publisher; `e2e-admin`, write on the administration repository: administrator;
  `e2e-reviewer`, triage: reviewer; `e2e-nobody`, read: no role, the administration stays closed),
  ask for a publisher, the administrator activates it, submit, every validator passes, the
  administrator approves, the catalog shows it, the index verifies (`fapps verify-index -key -min-serial`),
  yank shows in a new index; one language pack and one interface app per browser. On the interface
  app the same run also makes it a paid app on Price and licenses, makes a license key (shown once),
  checks it at `POST /v1/licenses/verify` (the verdict's signature checked by a Node verifier written
  apart from the Go code), makes an install link with that key through the Install dialog, reads its
  signed intent and completes it (a second completion answers 410), finds the saved instance on My
  filex instances, then revokes the key; and Administration > Apps filters apps by status. The
  interface app's fixture opens `.e2etxt`, `.drawio` and `.docx`, so the same run searches the
  catalog by file type once it is approved: `.docx` shows filex's own support ("with ONLYOFFICE")
  and the app, `drawio` finds it in the grid and in the list view, `.xyz` gives the empty state,
  and the app page's "Works with" leads back to the search. Its listing also declares two
  conversions: the reviewer sees them before approving, `.docx` says "Converts .docx → .pdf, .txt"
  on its card, `.e2edoc` (named by nothing but a conversion) finds it among the apps for the kind,
  the app page's conversion table lists both rows, and the signed index carries none of it.
- **Web unit tests** (vitest, happy-dom, `web/tests`): the pages and components against a mocked
  `fetch`, and **source guards** that read `web/src`: no table but filex's `DataTable` and every one
  under its own id, no colour that is not a filex token, no request to another host, no secret, no
  native `<select>`, one `<h1>` per page (`guards.test.ts`), and "license" spelled the American way
  in `web/src`, the documents and the OpenAPI file (`spelling.test.ts`).
- **Browser round** (`web/browser`, Playwright in the same image, Chromium, Firefox and WebKit,
  against the built bundle and the development mock, `web/mock`): every page light and dark at 1280
  and 375 px (no sideways scroll, filex's palette applied, axe's WCAG 2.1 A/AA rules in Chromium,
  screenshots for review that are not committed) and the interactive flows (the Install dialog,
  licenses, `FaSelect` in the catalog and inside the Install dialog, the theme switch, a file type
  typed in the search, "Works with" on an app page, ...); the mock answers a few file-type searches
  with the apps' file types and the very `filex-builtin.json` the server embeds; with
  `FAPPS_REAL_URL`, `real.spec.ts` runs the pages against the real Go server and its own headers
  (CSP).
- **Where**: in Docker, never on the host itself. The pre-push hook checks a copy of the pushed
  commit in named volumes (never a bind mount of the working tree), each container limited in CPU
  and memory ([DEPLOY.md](DEPLOY.md#the-pre-push-hook)); CI runs the same scripts on every pull
  request (`.github/workflows/ci.yml`), the end-to-end stack by hand (`e2e.yml`).
- **Red first**: every new test is shown failing on the code before the change (a real `FAIL`, not a
  compile error), then passing.

## Phases

| Phase | Content | Exit |
|---|---|---|
| **0** (done, 2026-10-03) | architecture, OpenAPI, schema draft, skeleton, work plan | skeleton builds (`go build`, `go test`, `pnpm build`), schema applies to Postgres 16 |
| **1** (**done**: a demo on 2026-10-04, the reference store since 2026-10-05) | database layer and migrations runner; GitHub sign-in (roles from the administration repository since 2026-10-04; the Keycloak sign-in was removed); publishers; submissions with the full pipeline (resolve + dry runs); review; approve publishes a signed index; catalog, app, publisher pages; media mirror; demo keys; yank/revoke; audit log; a first deployment; first entries (filex-sign, filex-convert, filextext, filex-drawio, the language packs) through the pipeline, by the [operator import](#operator-import-official-publisher) | e2e green; the store answers; `fapps verify-index` passes against it |
| **2** | key rotation UI, `/v1/releases` history, publisher API tokens and a GitHub release webhook (submit on tag), reproducible-build badge, identity linking, POLICY/PUBLISHING/SECURITY documents, stats | |
| **1+** (2026-10-04) | licenses for paid apps (keys, the signed check, activations), install links with a magic link, saved instances; the price on the catalog. **No payment and no automatic purchase**: the store shows a price and a purchase address, and the publisher sends the key (taking payments is phase 3, on purpose) | **done**: backend, web and e2e on `main`; live on the demo from 2026-10-04 (`FAPPS_DATA_KEY` and the demo license key set, `/readyz` without warnings), in the reference store since 2026-10-05 |
| **3** | the filex client (Store tab, the page a magic link opens, the license check, update notices) in the filex repository; payments; production domain and keys (**done** 2026-10-05: `apps.filex.sh`); artifact mirror; the open repository (decided 2026-10-26: MIT, `BRF-Tech/filex-apps`, anyone can run a store) | |

## Open questions

Q1 to Q13, all but Q11, were **decided by the owner on 2026-10-03 by taking the recommendation** below. Q11 was
deferred on purpose and decided on 2026-10-26. Q14 was decided by the owner on 2026-10-04. What the decision means in the code is noted where it differs from the
recommendation.

| # | Question | Decision (recommendation taken) |
|---|---|---|
| **Q1** | "Automatic installation with a magic link": may it install with nobody reviewing on the filex side, or only pre-fill / queue an install request? | **Decided:** pre-fill or queue (keeps filex's rule that an install is a person's decision). Built 2026-10-04 as the pre-fill: the magic link opens filex's install review, a filex administrator confirms ([Install links](#install-links-magic-link)) |
| **Q2** | How do the demo's secrets reach the container? | **Decided:** through the environment only, never a file in the repository. The demo is retired (2026-10-05); a store reads its secrets from its own `.env` and `keys.env` ([DEPLOY.md](DEPLOY.md#secrets-and-keys)) |
| **Q3** | Production key custody | **Decided and live (2026-10-05):** online for the artifact key (signs at approval), online for the index key with a hardware/offline option later; offline backups of all three (index, artifact, license) and of `FAPPS_DATA_KEY`, held by the owner. The demo kept its throw-away keys (demo retired 2026-10-05) |
| **Q4** | Production domain | **Decided and live (2026-10-05):** `apps.filex.sh`, the reference store, after the demo (demo retired 2026-10-05) |
| **Q5** | Organization publishers: how is ownership proven? | **Decided:** phase 1, an administrator activates org publishers by hand; every submission still needs repository `admin`/`maintain` and the consent file. `read:org` only if self-service org publishers are wanted |
| **Q6** | Language packs have no tags; the store accepts tags only | **Decided:** tag them (signed tags by the owner) before listing. **Done**: `v0.1.8` of filex-lang-es, -de and -fr are signed tags (GitHub: verified), listed on the demo since 2026-10-04 and in production since 2026-10-05 |
| **Q7** | Which filex versions do the validators cover? | **Decided:** `latest` = newest release, `floor` = previous minor (today 0.57.3 and 0.56.0; the plugin validator `plugin-latest` on latest's image); bump both at each filex release; apps whose range admits neither fail `no_validator_in_range` |
| **Q8** | Artifact mirror (`/v1/a/<sha256>`) for air-gapped filex | **Decided:** phase 3, with object storage (Garage) rather than Postgres |
| **Q9** | Four-eyes review while the owner is the only reviewer and also the official publisher | **Decided:** an admin may approve their own publisher's submissions in phase 1, flagged in the audit log; enforce four-eyes once there is a second reviewer |
| **Q10** | Listing metadata source | **Decided:** the repository's `.filex-apps.json` (pinned to the commit, reviewed); the web form shows a live preview of it |
| **Q11** | License of this repository (if it opens) | **Decided (2026-10-26): MIT** ([`LICENSE`](../LICENSE)), one open repository where development happens |
| **Q12** | Base images from Docker Hub | **Decided:** the [`Dockerfile`](../Dockerfile) takes them as build arguments (`GO_IMAGE`, `NODE_IMAGE`, `RUNTIME_IMAGE`), so a registry mirror can stand in for Docker Hub without editing it |
| **Q13** | UI language | **Decided:** English only at launch (open-source surface); Turkish later if wanted for the review screens |
| **Q14** | Where do the reviewer and administrator roles come from, and does staff need a sign-in of its own? | **Decided by the owner (2026-10-04)**, replacing the first plan's Keycloak sign-in: one sign-in, GitHub, for everyone; the roles come from the account's permission on a private administration repository (`admin` / `maintain` / `write`: administrator, `triage`: reviewer), read at every sign-in with a server token that fails closed; since 2026-10-26 also from a list of numeric GitHub user ids (`FAPPS_ADMINS`, `FAPPS_REVIEWERS`), so a store needs no repository for its staff ([Roles](#roles-the-administration-repository)) |
