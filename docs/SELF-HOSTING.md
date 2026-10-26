# Running your own filex app store

filex trusts any app store its administrator chooses to trust, so anyone can run one: for a
company's own apps, for a community, or to carry a reviewed selection. This page sets one up from
nothing, step by step: the store, its database, the filex servers it checks every submission with,
its GitHub sign-in, its keys, and the filex servers that will trust it.

[`deploy/compose/`](../deploy/compose/) is the example this page uses. Keeping a store running
afterwards (updates, the validators at each filex release, the operator commands) is
[DEPLOY.md](DEPLOY.md); how it works inside, [ARCHITECTURE.md](ARCHITECTURE.md).

## Contents

1. [What you will run](#what-you-will-run)
2. [Requirements](#requirements)
3. [Get the files](#get-the-files)
4. [Create the GitHub OAuth App](#create-the-github-oauth-app)
5. [Choose the administrators](#choose-the-administrators)
6. [Fill in the settings](#fill-in-the-settings)
7. [Make the store's keys](#make-the-stores-keys)
8. [The reverse proxy](#the-reverse-proxy)
9. [Start it](#start-it)
10. [The first publishers and apps](#the-first-publishers-and-apps)
11. [Publish your keys' fingerprints](#publish-your-keys-fingerprints)
12. [Let filex trust your store](#let-filex-trust-your-store)
13. [Backups](#backups)
14. [Updating](#updating)
15. [Settings reference](#settings-reference)

## What you will run

```
browser, filex --HTTPS--> your reverse proxy (TLS for apps.example.com)
                --HTTP---> 127.0.0.1:8080 -> app (ghcr.io/brf-tech/filex-apps)
                             +-- postgres       (PostgreSQL 16: every durable thing, media included)
                             +-- redis          (Redis 7: rate-limit counters only)
                             +-- filex-latest   (ghcr.io/brf-tech/filex, the newest release)  \ the validators:
                             +-- filex-floor    (ghcr.io/brf-tech/filex, the previous minor)  / dry runs
                             +-- plugin-latest  (filex's storage plugin validator, no network)
app --HTTPS--> GitHub (sign-in, the REST API, files at a commit, release assets)
```

The store reads every submission from GitHub at a tag, has real filex servers review it the way filex
itself installs it (the **validators**: they hold no data and take no traffic from outside), lets a
reviewer approve it, and publishes a **signed index** that filex servers verify with keys they
trust. Everyone signs in with GitHub.

## Requirements

- A Linux host with Docker Engine and Docker Compose v2. Memory: the example's limits add up to about
  3 GB (app 256 MiB, Postgres 384 MiB, Redis 64 MiB, each of the three validators 768 MiB); disk: each
  filex image is about 0.8 GB, plus the database (the media mirror lives in it).
- A domain name for the store (this page: `apps.example.com`) and a reverse proxy with TLS in front
  of it (Caddy, nginx, Traefik, ...).
- Outbound HTTPS to GitHub (`github.com`, `api.github.com`, `raw.githubusercontent.com`,
  `objects.githubusercontent.com`, `release-assets.githubusercontent.com`,
  `github-releases.githubusercontent.com`, `codeload.github.com`, `avatars.githubusercontent.com`)
  and to `ghcr.io` for the images.
- A GitHub account (or organization) to own the store's OAuth App.

PostgreSQL 16 and Redis 7 come with the example compose; a store can use servers of its own instead
(`DATABASE_URL`, `REDIS_URL`).

## Get the files

```sh
mkdir -p /srv/fapps && cd /srv/fapps
curl -fsSLO https://raw.githubusercontent.com/BRF-Tech/filex-apps/main/deploy/compose/docker-compose.yml
curl -fsSL -o .env.example https://raw.githubusercontent.com/BRF-Tech/filex-apps/main/deploy/compose/.env.example
cp .env.example .env && chmod 600 .env
```

(or copy `deploy/compose/` out of a clone of this repository). The steps below fill `.env` in.

## Create the GitHub OAuth App

Everyone signs in with GitHub: publishers (it also proves who owns a repository), reviewers and
administrators. Create an **OAuth App** (not a GitHub App) under your account or organization:
**Settings -> Developer settings -> OAuth Apps -> New OAuth App**.

| Field | Value |
|---|---|
| Application name | your store's name |
| Homepage URL | `https://apps.example.com` (your `FAPPS_PUBLIC_URL`) |
| Authorization callback URL | `https://apps.example.com/auth/github/callback` |

The store asks for **no scopes**: the person's public profile and public repositories are all it
reads with their token. Generate a client secret and put both values in `.env`:

```sh
FAPPS_GITHUB_CLIENT_ID=Ov23li...          # not a secret
FAPPS_GITHUB_CLIENT_SECRET=...            # a secret
```

**Optional, recommended:** a token for the store's own GitHub reads (the pipeline, publishers'
avatars). Without it they share GitHub's anonymous limit, 60 requests an hour per address. A
fine-grained personal access token with **public repositories, read-only** and no other permission;
an organization may refuse tokens that live long (keep its lifetime within the policy, or the store
repeats such reads anonymously). `FAPPS_GITHUB_TOKEN=...` in `.env`.

## Choose the administrators

The store grants no role by itself. A **reviewer** approves or rejects submissions; an
**administrator** can also activate publishers, list, yank and revoke apps, and see the keys, the
validators and the audit log. Name them in one of two ways (or both: an account named in more than
one place gets the highest of its roles):

- **A list in `.env`**: numeric GitHub user ids, comma-separated, each optionally labelled with its
  login: `FAPPS_ADMINS=alice:1234567,7654321`, `FAPPS_REVIEWERS=bob:2345678`. A bare login is
  refused: logins can be renamed and taken by someone else, ids cannot. To find an id:
  `curl -s https://api.github.com/users/<login>` (its `"id"`) or `gh api users/<login> --jq .id`.
  A change takes effect when the store restarts (`docker compose up -d app`).
- **A private GitHub repository** whose collaborators are the staff: `admin`, `maintain` or `write`
  make an administrator, `triage` a reviewer. `FAPPS_ADMIN_REPO=<owner>/<name>` and
  `FAPPS_ADMIN_TOKEN`: a fine-grained token owned by the repository's owner, repository access to
  that repository **only**, the single permission **Metadata: read**. Staff then change on GitHub,
  without touching the store. If the token cannot see the repository, nobody gets a role from it and
  `/readyz` says so (it fails closed).

Either way the role is read at every sign-in and kept for the session: a session with a role lasts
12 hours, so a role taken away ends at the next sign-in at the latest. With neither set the store
does not start (a configuration error naming both settings): a store nobody can review would take
submissions that never leave the queue. Only `APP_ENV=development` starts without them, with a
warning.

## Fill in the settings

The rest of `.env` ([`.env.example`](../deploy/compose/.env.example) explains every line):

```sh
FAPPS_VERSION=0.1.0                        # a release of ghcr.io/brf-tech/filex-apps
FAPPS_PUBLIC_URL=https://apps.example.com  # where people and filex servers reach the store
FAPPS_STORE_NAME=Example Apps              # default: the host of FAPPS_PUBLIC_URL
FAPPS_OPERATOR_NAME=Example Ltd            # who runs it, in the footer
FAPPS_OPERATOR_URL=https://example.com
FAPPS_CONTACT=mailto:apps@example.com      # or an https address
FAPPS_TERMS_URL=https://example.com/terms  # optional
FAPPS_PRIVACY_URL=https://example.com/privacy
FAPPS_DB_PASSWORD=...                      # openssl rand -hex 24
FAPPS_VALIDATOR_PASSWORD=...               # openssl rand -hex 16
```

Your logo: put an SVG or PNG in `./branding/`, uncomment the `./branding:/branding:ro` mount in
`docker-compose.yml` and set `FAPPS_STORE_LOGO=/branding/logo.svg`.

Every absolute address the store writes (the index's media, the OAuth callback, install links) is
built from `FAPPS_PUBLIC_URL`, never from request headers, so it must be the address people use.

## Make the store's keys

The store signs with three ed25519 keys (the **index** key: the index and install links; the
**artifact** key: app modules, interface bundles and storage plugin builds; the **license** key:
license answers) and needs two random secrets (`FAPPS_DATA_KEY`: license keys, install links, saved
filex addresses and notification channels are sealed under it; `FAPPS_SESSION_KEY`: cookies) and a
Web Push key pair for browser notifications. One command makes all of them, once:

```sh
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/out" \
  ghcr.io/brf-tech/filex-apps:0.1.0 keygen -out /out/keys.env
```

It writes `keys.env` (mode 0600; the compose file reads it next to `.env`) and prints only what is
public: each key's id, public key and fingerprint, and the value a filex administrator can put in
`FILEX_APP_STORE_KEYS` ([Let filex trust your store](#let-filex-trust-your-store)). Keep that output.

⚠ **Keep `keys.env` safe and never make it again for a store that has data.** A new `FAPPS_DATA_KEY`
makes every license key `invalid` and every saved filex address and webhook unreadable; new signing
keys are a different store to every filex that trusted the old ones. Copy `keys.env` to where your
secrets live (a password manager, an offline copy), apart from the database backups.

## The reverse proxy

The app listens on `127.0.0.1:8080` (`FAPPS_BIND`). Your proxy ends TLS and forwards everything to it.
Caddy (which gets the certificate itself):

```caddyfile
apps.example.com {
	reverse_proxy 127.0.0.1:8080
}
```

nginx:

```nginx
server {
    listen 443 ssl;
    server_name apps.example.com;
    ssl_certificate     /etc/letsencrypt/live/apps.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/apps.example.com/privkey.pem;
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto https;
    }
}
```

- The store sets its own security headers (HSTS, a strict CSP, `X-Frame-Options`, `nosniff`); do not
  add a second HSTS header in the proxy.
- The store takes the visitor's address (for its rate limits) from the rightmost `X-Forwarded-For`
  hop when the connection comes from a loopback or private address, which is what a proxy on this
  host is: leave `FAPPS_TRUSTED_PROXIES` empty. With a CDN in front of the proxy, see
  [DEPLOY.md, "Client addresses"](DEPLOY.md#client-addresses).
- A page counter: the store prints none, but `FAPPS_ANALYTICS_ORIGIN=https://stats.example.com` lets
  the CSP load and report to a counter script your proxy adds to the pages.

## Start it

```sh
docker compose pull
docker compose up -d
docker compose ps                                  # app healthy, the validators running
curl -fsS http://127.0.0.1:8080/healthz            # {"status":"ok","version":"0.1.0"}
curl -sS  https://apps.example.com/readyz          # 200 {"status":"ready"}, ideally without "warnings"
```

At its first start the store applies its migrations and publishes its first, empty signed index
(serial 1). `/readyz` answers 503 with a `missing` list while something required is absent (the
database, a signing key, a signed index) and carries `warnings` for what only keeps a feature off (no
`FAPPS_DATA_KEY`, no license key, a role check that sees no repository). The log says the same
(`docker compose logs app`).

Then sign in at `https://apps.example.com` with GitHub as one of your administrators:
**Administration** appears in the header. **Administration -> Validators** shows both validators and
the plugin validator as reachable, with their filex versions.

## The first publishers and apps

**The normal path**, for any publisher: a person signs in, asks for a publisher (**Publish -> Ask for
a publisher**), bound to their GitHub account or an organization; an administrator activates it
(**Administration -> Publishers**). The publisher then submits a public repository at a tag: the
repository holds `filex-app.json` and `.filex-apps.json`, the owner's consent to the listing and its
metadata ([schema](../schema/filex-apps.schema.json); the Submit page has a helper that writes it).
The store checks it, the validators dry-run it, a reviewer approves it, and it is in the signed index.

**Your own organization's apps**, or apps whose tags carry no `.filex-apps.json`: the operator
import lists the tags of a publisher you make **official and verified**, with a listing folder you
provide, through every check but the consent file
([DEPLOY.md, "Official apps"](DEPLOY.md#official-apps)):

```sh
docker compose exec app /fapps-server publisher -id acme -name "Acme" -github-owner acme-org
docker compose cp ./listings/acme-viewer app:/tmp/listing-acme-viewer-1
docker compose exec app /fapps-server import -publisher acme -repo acme-org/acme-viewer \
  -ref v1.0.0 -listing /tmp/listing-acme-viewer-1/listing.json -approve
```

The filex project's own apps (the office editor, filex-sign, filex-convert, the language packs, ...)
are public repositories; [`seed/official/`](../seed/official/) holds their listings and an
`import.sh` that imports them the same way, should your store carry them.

## Publish your keys' fingerprints

A filex administrator decides to trust your store by comparing the fingerprints of its keys with a
source they believe. Give them more than the store itself:

1. **The store**: `https://apps.example.com/v1/keys.json` lists every key (id, use, status, the
   public key as `ed25519` hex). The fingerprint is the lower-hex sha256 of the 32 raw public key bytes:
   `python3 -c "import hashlib,sys; print(hashlib.sha256(bytes.fromhex(sys.argv[1])).hexdigest())" <ed25519 hex>`.
2. **A page of yours outside the store** (your website, your documentation): a table of the key ids,
   their uses and fingerprints, as `keygen` printed them.
3. **Optionally, a signed copy**: a `keys/<store>.json` in a repository of yours, with a detached
   OpenPGP signature made with a key people already know you by (`gpg --detach-sign --armor`), so
   anyone can run `gpg --verify` and compare.

The reference store does all three: [`keys/production.json`](../keys/production.json) (+
[`.asc`](../keys/production.json.asc)) is `apps.filex.sh`'s keys, signed with the release key that
signs filex's tags (`EFA3B1262FD992800DBBB5E3A8FEBA97FF786513`, public half at
`https://github.com/brkfun.gpg`): `gpg --verify keys/production.json.asc keys/production.json`, then
the `sha256` values must equal those `https://apps.filex.sh/v1/keys.json` gives. filex's documentation
publishes the same table.

## Let filex trust your store

A filex server (0.52 and later) takes install links and license answers only from a store it trusts,
signed with the keys it trusts that store with (filex's
[APP-PLUGINS.md, "Trusted stores"](https://github.com/BRF-Tech/filex/blob/main/docs/APP-PLUGINS.md#trusted-stores)).
Its administrator has two ways:

- **On first use**: the first install link from your store (an app page's **Install**) opens a page in
  their filex that shows your store's address and its keys' fingerprints. They compare them with what
  you published, tick the box and press **Trust this store**. A store that later publishes another key
  gets the question again.
- **By configuration**, on the filex server: `FILEX_APP_STORE_URLS=https://apps.example.com` and
  `FILEX_APP_STORE_KEYS=index:<index public key>,license:<license public key>` (the line `keygen`
  printed). A configured store is never put to an administrator; a key it publishes that is not
  configured is refused. ⚠ Set, `FILEX_APP_STORE_URLS` is an allow list: no other store is trusted, not
  even on first use - list every store that filex should take links from.

To accept the modules your store signed, the filex administrator also adds your **artifact** public
key to `FILEX_PLUGIN_TRUSTED_KEYS` (that setting turns signature enforcement on for every plugin and
app). And to let the people of that filex browse your catalog inside filex and ask for apps (filex
0.53 and later), they **connect** it: on your store, **My instances -> Connect a filex server** gives
a one-time code, which their administrator pastes under **Admin -> Plugins -> Apps -> Trusted stores
-> Connect** (filex's [APP-PLUGINS.md, "Connecting a
store"](https://github.com/BRF-Tech/filex/blob/main/docs/APP-PLUGINS.md#connecting-a-store)).

## Backups

Two things make a store, and a backup needs both:

1. **The database**, which holds everything durable, the mirrored media included:
   `docker compose exec -T postgres pg_dump -U fapps --clean --if-exists fapps | gzip > fapps-$(date +%F).sql.gz`,
   on a schedule, kept off the host.
2. **`keys.env`** (and `.env`): `FAPPS_DATA_KEY` and the signing keys. A dump without them is
   incomplete: license keys, install links, saved addresses and webhooks cannot be read without
   `FAPPS_DATA_KEY`, and an index signed with other keys is not your store to any filex.

Restoring: the same `keys.env` and `.env`, `docker compose up -d postgres`, then
`gunzip -c fapps-<date>.sql.gz | docker compose exec -T postgres psql -U fapps -d fapps`, then
`docker compose up -d`. Never generate new keys for a restored store. Redis needs no backup.

## Updating

- **The store**: a new release in `FAPPS_VERSION`, a dump first, then
  `docker compose pull app && docker compose up -d --no-deps app`; migrations run at start and are
  forward-only. Some releases ask you to re-publish the index once (`publish-index`): their notes say
  so. Rollback and checks: [DEPLOY.md](DEPLOY.md#deploying-a-new-version).
- **The validators**, at each filex release: `filex-latest` and `plugin-latest` to the new release,
  `filex-floor` to the previous minor, then
  `docker compose up -d --no-deps filex-latest filex-floor plugin-latest`
  ([DEPLOY.md, "Validators"](DEPLOY.md#validators)). An app that needs the new filex fails
  `no_validator_in_range` until then.
- **Postgres and Redis**: patch versions by changing the image line; a new Postgres major needs a dump
  and restore.

## Settings reference

Every variable is read in `backend/internal/config`; [ARCHITECTURE.md,
"Configuration"](ARCHITECTURE.md#configuration) lists them all with their defaults. The ones a store
operator sets:

| Variable | Default | Secret | What |
|---|---|---|---|
| `FAPPS_PUBLIC_URL` (or `APP_URL`) | | | the store's origin, `https://host`; every absolute address is built from it |
| `FAPPS_STORE_NAME` | the host of `FAPPS_PUBLIC_URL` | | the store's name on its pages |
| `FAPPS_STORE_LOGO` | filex's mark | | a local SVG or PNG file (a path inside the container) shown as the store's logo |
| `FAPPS_OPERATOR_NAME`, `FAPPS_OPERATOR_URL` | none | | who runs the store, in the footer |
| `FAPPS_CONTACT` | none | | how to reach the operator: `mailto:` or `https:` |
| `FAPPS_TERMS_URL`, `FAPPS_PRIVACY_URL` | none | | the operator's terms and privacy pages |
| `FAPPS_ADMINS`, `FAPPS_REVIEWERS` | none | | numeric GitHub user ids (`12345` or `login:12345`; a bare login is refused) of the administrators and reviewers |
| `FAPPS_ADMIN_REPO` | none | | a private repository whose collaborators are the staff (`write` and above: administrator, `triage`: reviewer) |
| `FAPPS_ADMIN_TOKEN` | | **yes** | the token that reads `FAPPS_ADMIN_REPO`'s permissions (fine-grained, that repository only, Metadata: read) |
| `FAPPS_GITHUB_CLIENT_ID`, `FAPPS_GITHUB_CLIENT_SECRET` | | the secret | the GitHub OAuth App |
| `FAPPS_GITHUB_TOKEN` | | **yes** | optional: public read-only token for the store's GitHub reads |
| `FAPPS_INDEX_KEY`, `FAPPS_ARTIFACT_KEY`, `FAPPS_LICENSE_KEY` (+ `_KEY_ID`) | | **yes** | the signing keys (`keygen`) |
| `FAPPS_DATA_KEY`, `FAPPS_SESSION_KEY` | | **yes** | the data and session keys (`keygen`); `FAPPS_DATA_KEY` is never changed on a store with data |
| `FAPPS_VAPID_PUBLIC_KEY`, `FAPPS_VAPID_PRIVATE_KEY` | | the private key | the Web Push pair (`keygen`); without it browser notifications are off |
| `DATABASE_URL`, `REDIS_URL` | set by the example compose | the URL's password | PostgreSQL 16 (required) and Redis 7 (optional) |
| `FAPPS_VALIDATORS`, `FAPPS_PLUGIN_VALIDATORS` | set by the example compose | | the validators' addresses and the plugin validator's spool folder |
| `FAPPS_VALIDATOR_PASSWORD` | | no | the validators' throw-away administrator |
| `FAPPS_TRUSTED_PROXIES` | empty | | peers whose `X-Forwarded-For` is believed ([DEPLOY.md](DEPLOY.md#client-addresses)) |
| `FAPPS_ANALYTICS_ORIGIN` | empty | | a page counter's origin the CSP allows |
| `FAPPS_INDEX_TTL_DAYS`, `FAPPS_INDEX_REFRESH_DAYS` | 60, 30 | | how long an index is valid; it is re-signed once fewer days are left |

`keygen` is `fapps-server keygen -out <file>`: it writes the index, artifact and license seeds with
their key ids, `FAPPS_DATA_KEY`, `FAPPS_SESSION_KEY` and the VAPID pair to the file (mode 0600) and
prints only the public keys, their fingerprints and the `FILEX_APP_STORE_KEYS` value.
