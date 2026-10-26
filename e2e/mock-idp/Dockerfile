# syntax=docker/dockerfile:1
#
# The end-to-end stack's mock of GitHub (TEST-ONLY). Build
# context: e2e/ (the tests read the real fixtures). vet and the unit tests run
# in the build, so a broken mock or fixture never reaches a test run.
ARG GO_IMAGE=golang:1.25-bookworm
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12

FROM ${GO_IMAGE} AS build
WORKDIR /src/mock-idp
COPY mock-idp/ ./
COPY fixtures/ /src/fixtures/
ENV GOFLAGS=-buildvcs=false CGO_ENABLED=0
RUN test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
RUN go vet ./... && go test -count=1 ./...
RUN go build -trimpath -ldflags "-s -w" -o /out/mock-idp .

# Root on purpose: edge and github-tls bind port 443 inside the namespaces
# they join. Nothing in this image runs outside the test stack.
FROM ${RUNTIME_IMAGE}
COPY --from=build /out/mock-idp /mock-idp
COPY fixtures/ /fixtures/
ENTRYPOINT ["/mock-idp"]
CMD ["serve"]
