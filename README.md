# filex Apps

The app store for [filex](https://github.com/BRF-Tech/filex), and the software to run one of your
own: a catalog of filex apps, language packs and storage plugins, a publishing pipeline that checks
every release the way filex itself installs it, human review, and a **signed index** that filex
servers verify with keys they trust.

The reference store is [apps.filex.sh](https://apps.filex.sh). Anyone can run another one - for a
company's own apps, a community, or a reviewed selection - and a filex administrator decides which
stores their filex trusts: [docs/SELF-HOSTING.md](docs/SELF-HOSTING.md).

## How it works

1. **Publishing.** A publisher signs in with GitHub and submits a public repository at a **tag**. The
   repository holds `filex-app.json` (the app's manifest) and `.filex-apps.json` (the owner's consent
   to the listing and the listing's metadata, [schema](schema/filex-apps.schema.json)).
2. **Checks.** The store resolves the tag to a commit, reads the manifest there, downloads the module
   and the interface bundle (or a storage plugin's builds) and checks their SHA-256, mirrors the
   screenshots, and asks **real filex servers** for their install review (a dry run) at the newest
   supported filex and the oldest; a storage plugin's build runs on filex's plugin validator, with no
   network.
3. **Review.** A reviewer reads the result and approves or rejects. Everyone signs in with GitHub; the
   reviewers and administrators are named by the store's operator (GitHub user ids, or the
   collaborators of a private repository).
4. **The signed index.** On approval the store signs the version's artifacts and publishes a new
   index (`/v1/index.json` + `/v1/index.json.sig`, ed25519, the scheme filex uses for plugin
   signatures), with a growing `serial` and an expiry, so a filex refuses an old or a frozen one.
5. **Install.** On an app's page, Install makes a one-time link to the person's own filex (valid 30
   minutes, the token in the URL fragment); that filex reads the signed install intent and shows its
   own install review. A filex server can also be **connected** to the store: its people browse the
   catalog inside filex and ask for apps, and its administrator approves.
6. **Paid apps.** A publisher marks an app paid (the store shows the price; it takes no payment) and
   makes **license keys** for buyers. filex checks a key with `POST /v1/licenses/verify` and gets a
   verdict signed with the store's license key; the store counts the filex servers using each key
   against its seats, and keeps keys only as an HMAC.
7. **Find by file type.** A search for `.docx` (or `docx`, `pdf docx`) says what filex does with that
   kind by itself and lists the apps that open it, make it, draw its thumbnails, offer an action on it
   or convert it, each saying which.
8. **Notifications.** A signed-in person chooses which events reach them, in the browser (Web Push) or
   on webhooks (generic JSON signed with HMAC-SHA256, Slack, Discord, Teams).

The details, threat by threat: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Run your own store

```sh
cd deploy/compose
cp .env.example .env            # your domain, name, GitHub OAuth App, administrators
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/out" \
  ghcr.io/brf-tech/filex-apps:<version> keygen -out /out/keys.env
docker compose up -d            # the store, PostgreSQL, Redis, the filex validators
```

Then a reverse proxy with TLS in front of it, and your keys' fingerprints published for the filex
administrators who will trust it. Step by step: [docs/SELF-HOSTING.md](docs/SELF-HOSTING.md).

**The reference store's keys.** [`keys/production.json`](keys/production.json) holds
`apps.filex.sh`'s public keys and fingerprints, with a detached signature
([`.asc`](keys/production.json.asc)) by the release key that signs filex's tags: the second source an
administrator checks before trusting it
([SELF-HOSTING.md](docs/SELF-HOSTING.md#publish-your-keys-fingerprints)).

## Documentation

| Document | What |
|---|---|
| [docs/SELF-HOSTING.md](docs/SELF-HOSTING.md) | running a store of your own: requirements, the GitHub OAuth App, administrators, keys, the reverse proxy, the first apps, publishing fingerprints, filex's trust, backups, updates |
| [docs/DEPLOY.md](docs/DEPLOY.md) | keeping it running: new versions, rollback, checks, keys, the validators at each filex release, the plugin validator, the operator commands, the generated data files, building and testing |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | how the store works and why: identity and roles, the pipeline, the index, signatures, licensing, install links, file-type search, notifications, security, configuration |
| [api/openapi.yaml](api/openapi.yaml) | the HTTP contract (where it and a document disagree, the contract is right) |
| [CONTRIBUTING.md](CONTRIBUTING.md), [SECURITY.md](SECURITY.md) | how to contribute; how to report a vulnerability |

## Repository layout

| Path | What |
|---|---|
| `backend/` | the Go server (`cmd/server`: API, sign-in, pipeline, signed index, embedded web interface, operator commands) and the operator CLI (`cmd/fapps`: `keygen` for one key, `sign`, `verify-index`, `vectors`) |
| `web/` | the Vue 3 + Vite + TypeScript web interface |
| `api/openapi.yaml` | the HTTP contract |
| `schema/` | the JSON Schema of `.filex-apps.json` |
| `deploy/compose/` | a production compose project: the store, PostgreSQL 16, Redis 7, the validators |
| `seed/official/` | the listings of the filex project's own apps and `import.sh`, for the operator import |
| `keys/` | the reference store's public keys, signed |
| `e2e/`, `docker-compose.yml`, `docker-compose.e2e.yml` | the end-to-end stack: the store with real filex validators, a mock of GitHub, Playwright in three browsers |
| `scripts/` | the checks (`checks/`), the integration and end-to-end runners, the signature cross-check against filex (`xcheck/`), the generators of filex's own file types and of filex-convert's conversions |
| `Dockerfile` | the image: web build, Go build, a distroless runtime |

## Building

The image builds everything:

```sh
docker build -t filex-apps .
```

Separately (Go 1.25, Node 22, pnpm 9.12.0), or in Docker with the same scripts CI runs:

```sh
cd backend && go build ./... && go test ./...
cd web && pnpm install && pnpm gen:api && pnpm build && pnpm test
bash scripts/integration.sh     # PostgreSQL 16 + Redis 7 integration tests, in Docker
bash scripts/e2e.sh             # the end-to-end stack, in Docker
```

`pnpm gen:api` regenerates `web/src/api/schema.d.ts` from `api/openapi.yaml`. The pre-push hook
(`bash .githooks/install.sh`) runs gitleaks and the Go and web checks in Docker before every push
([CONTRIBUTING.md](CONTRIBUTING.md)).

## Configuration

Environment variables only: the ones a store operator sets are in
[docs/SELF-HOSTING.md](docs/SELF-HOSTING.md#settings-reference), every one with its default in
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md#configuration) and `backend/internal/config`. Secrets
never go into a repository.

## License

[MIT](LICENSE). Copyright (c) 2026 BRF Tech.
