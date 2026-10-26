<!-- Thanks for contributing to filex Apps! -->

## What and why

<!-- What does this change do, and why? Link the issue it closes: Fixes #123 -->

## Type

- [ ] Bug fix
- [ ] Feature
- [ ] Docs
- [ ] Refactor / chore

## Checklist

- [ ] The pre-push checks pass (`bash .githooks/install.sh` once; they run on every push), or `scripts/checks/go.sh` and `scripts/checks/web.sh` by hand
- [ ] Integration tests where the database or Redis is involved: `bash scripts/integration.sh`
- [ ] Tests for the change, shown failing before it and passing after
- [ ] `api/openapi.yaml` and `web/src/api/schema.d.ts` (`pnpm gen:api`) together, if the HTTP contract changed
- [ ] Docs (`docs/`, `README.md`) if what people see or configure changed
- [ ] No secrets, credentials or host names of a real deployment

## Notes for reviewers

<!-- Trade-offs, follow-ups, screenshots. -->
