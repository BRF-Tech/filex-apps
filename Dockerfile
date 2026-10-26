# syntax=docker/dockerfile:1
#
# filex app store: one image, one process (API + workers + embedded SPA).
#
# Base images are build arguments so a registry mirror can stand in for
# Docker Hub without editing this file. The runtime image carries CA
# certificates (the server talks HTTPS to GitHub) and no shell.
#
# Multi-platform (docker buildx build --platform linux/amd64,linux/arm64):
# the web bundle and the server are built on the build machine's own
# platform ($BUILDPLATFORM) and the server cross-compiled for each target
# (pure Go, CGO off), so no stage runs under emulation; only the runtime
# stage is per platform.
ARG NODE_IMAGE=node:22-bookworm-slim
ARG GO_IMAGE=golang:1.25-bookworm
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot

# ---------------------------------------------------------------- web
FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS web
WORKDIR /src/web
RUN corepack enable && corepack prepare pnpm@9.12.0 --activate
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY api/ /src/api/
COPY web/ ./
RUN pnpm gen:api && pnpm build

# ---------------------------------------------------------------- server
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS server
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src/backend
COPY backend/go.* ./
RUN go mod download
COPY backend/ ./
COPY --from=web /src/web/dist/ ./internal/webui/dist/
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X github.com/brf-tech/filex-apps/backend/internal/httpapi.Version=${VERSION}" \
      -o /out/fapps-server ./cmd/server

# ---------------------------------------------------------------- runtime
FROM ${RUNTIME_IMAGE}
COPY --from=server /out/fapps-server /fapps-server
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/fapps-server", "healthcheck"]
ENTRYPOINT ["/fapps-server"]
