# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32
FROM golang:1.26.10-alpine@sha256:5cb67f19c5e7994dcb80c074f7cfca8aeeada45bbdca035f73df269cefdcce61 AS build
ARG VERSION=local
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w -X github.com/ml8s/liki-agent-runtime/internal/platform/buildinfo.Version=${VERSION} -X github.com/ml8s/liki-agent-runtime/internal/platform/buildinfo.Commit=${COMMIT} -X github.com/ml8s/liki-agent-runtime/internal/platform/buildinfo.BuildTime=${BUILD_TIME}" -o /out/liki-agent-runtime ./cmd/liki-agent-runtime
RUN mkdir -p /data && chown 65532:65532 /data

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG VERSION=local
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
COPY --from=build /out/liki-agent-runtime /liki-agent-runtime
COPY --from=build --chown=nonroot:nonroot /data /data
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_TIME}" \
      org.opencontainers.image.title="liki-agent-runtime" \
      org.opencontainers.image.description="Domain-neutral Liki multi-agent runtime"
ENV LIKI_ENV=production LIKI_AGENT_RUNTIME_AUDIT_DB_PATH=/data/liki-agent-runtime-audit.db
VOLUME ["/data"]
EXPOSE 8083 8085
USER nonroot:nonroot
ENTRYPOINT ["/liki-agent-runtime"]
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD ["/liki-agent-runtime", "healthcheck"]
