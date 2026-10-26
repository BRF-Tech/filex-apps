# syntax=docker/dockerfile:1
#
# The e2e test runner: Playwright with its three browsers, the test project,
# and the store's operator CLI `fapps` (backend/cmd/fapps, for
# `fapps verify-index`). Build context: e2e/; the backend comes in as the
# named context "backend" (docker-compose.e2e.yml additional_contexts).
ARG GO_IMAGE=golang:1.25-bookworm
ARG PLAYWRIGHT_IMAGE=mcr.microsoft.com/playwright:v1.59.1-noble

FROM ${GO_IMAGE} AS fapps
COPY --from=backend . /src/backend
WORKDIR /src/backend
ENV GOFLAGS=-buildvcs=false CGO_ENABLED=0
# The CLI is the validator lane's; until it is on main the runner says so
# and the specs that need it skip with that reason.
RUN --mount=type=cache,target=/go/pkg/mod \
    mkdir -p /out && \
    if [ -d cmd/fapps ]; then go build -trimpath -o /out/fapps ./cmd/fapps; \
    else echo "backend/cmd/fapps is not on this branch yet" > /out/fapps.missing; fi

FROM ${PLAYWRIGHT_IMAGE}
WORKDIR /e2e
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
COPY --from=fapps /out/ /opt/fapps/
ENV PATH=/opt/fapps:$PATH HOME=/root
CMD ["npx", "playwright", "test"]
