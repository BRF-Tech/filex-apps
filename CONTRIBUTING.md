# Contributing to filex Apps

Thanks for helping! The store is a Go server (`backend/`) with a Vue 3 web
interface (`web/`), a PostgreSQL database and real filex servers as its
validators. How it fits together: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
Where the HTTP contract and the code disagree, `api/openapi.yaml` is right.

## Build and test

Everything runs in Docker; nothing needs Go or Node on your machine, though
both work (Go 1.25, Node 22, pnpm 9.12.0):

| What | Command |
|---|---|
| Go: gofmt, vet, build, unit tests | `cd backend && bash ../scripts/checks/go.sh` (or in `golang:1.25`) |
| Web: install, `gen:api`, build, unit tests | `cd web && bash ../scripts/checks/web.sh` (or in `node:22-bookworm-slim`) |
| Integration tests (PostgreSQL 16, Redis 7) | `bash scripts/integration.sh` |
| End-to-end (the stack, three browsers) | `bash scripts/e2e.sh` |
| The image | `docker build -t filex-apps .` |

`pnpm gen:api` regenerates `web/src/api/schema.d.ts` from `api/openapi.yaml`:
change both together. More on the end-to-end stack, the validator integration
tests and the signature cross-check: [docs/DEPLOY.md](docs/DEPLOY.md#building-and-testing).

## The pre-push hook

```sh
bash .githooks/install.sh
```

Every push then runs gitleaks over the pushed commits and the Go and web
checks above, in Docker, from a copy of the pushed commit (never your working
tree); the push goes ahead only if all pass. `FAPPS_PREPUSH_HOST=ssh:<host>`
runs them on another machine's Docker. CI runs the same scripts on every pull
request (`.github/workflows/ci.yml`).

## Commits and pull requests

- English everywhere: code, comments, documents, commit messages.
- Conventional commits: `feat(scope): ...`, `fix(scope): ...`, `docs: ...`,
  `chore: ...`, `test: ...`; the subject says what changed, the body why.
- A test for every change, shown failing on the code before the change and
  passing after it.
- Migrations are forward-only and additive: never edit one that has run.
- No secrets, tokens or the host names of a real deployment in code, tests or
  documents; use `example.com` addresses and made-up people (alice, bob).
- Every table in the web interface is filex's `DataTable`, every colour a
  filex token, every choice `FaSelect` or `FaSegmented` (never a native
  `<select>`): `web/tests/guards.test.ts` checks it.

Security problems: privately, never in an issue ([SECURITY.md](SECURITY.md)).
Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md).
