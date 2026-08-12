# @author Kurok1 <im.kurokyhanc@gmail.com>
# @since v1.5.1
# syntax=docker/dockerfile:1.7

# ---------- Stage 1: build the embedded SPA ----------
# Vite writes its output to ../internal/web/dist per frontend/vite.config.ts,
# so the working layout here mirrors the repo root.
FROM node:22-alpine AS frontend-builder
WORKDIR /app
COPY frontend/package.json frontend/package-lock.json ./frontend/
WORKDIR /app/frontend
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# ---------- Stage 2: compile the Go server ----------
# go-duckdb v2 links a vendored libduckdb via CGO, so we need a glibc-based
# toolchain image. The resulting binary statically links DuckDB and only
# depends on glibc at runtime — no external duckdb install required.
FROM golang:1.26-bookworm AS go-builder
ENV CGO_ENABLED=1 \
    GOOS=linux
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
# Replace any host-side dist (it is .dockerignored) with the freshly built SPA
# so //go:embed all:dist embeds the in-container artifact.
RUN rm -rf internal/web/dist
COPY --from=frontend-builder /app/internal/web/dist ./internal/web/dist

ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath \
      -ldflags="-s -w -X github.com/kuroky/claude-code-monitor/internal/buildinfo.version=${VERSION}" \
      -o /out/server ./cmd/server

# Bundle the LiteLLM price table into the image (fetched at build time, NOT
# vendored in the repo). config.docker.yaml's pricing.source_file points here.
# curl ships in the golang buildpack-deps base. Fail the build if unreachable.
ARG LITELLM_PRICING_URL=https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json
RUN mkdir -p /out/pricing \
    && curl -fsSL --retry 5 --retry-delay 2 -o /out/pricing/litellm.json "${LITELLM_PRICING_URL}"

# ---------- Stage 3: minimal runtime ----------
FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*

COPY --from=go-builder /out/server /usr/local/bin/server
# Container-ready default config (binds 0.0.0.0, points DuckDB at /data)
COPY config.docker.yaml /etc/claude-code-monitor/config.yaml
# Reference copy of the upstream example for users mounting their own config
COPY config.example.yaml /etc/claude-code-monitor/config.example.yaml
# Bundled LiteLLM price table (fetched during build). Enable via the `pricing`
# block in config; source_file already points at this path in config.docker.yaml.
COPY --from=go-builder /out/pricing/litellm.json /etc/claude-code-monitor/pricing/litellm.json

# Persist DuckDB files (and capture dir, if enabled) outside the container
VOLUME ["/data"]

# 4317 = OTLP gRPC ingest, 9100 = stats + dashboard API + embedded web UI
EXPOSE 4317 9100

ENV TZ=Asia/Shanghai \
    CLAUDE_CODE_MONITOR_NO_UPDATE_CHECK=1

ENTRYPOINT ["/usr/local/bin/server"]
CMD ["-config", "/etc/claude-code-monitor/config.yaml"]
