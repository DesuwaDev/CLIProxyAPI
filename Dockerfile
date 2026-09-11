# syntax=docker/dockerfile:1
FROM oven/bun:1.3.14-slim AS frontend
WORKDIR /ui
COPY management-ui/package.json management-ui/bun.lock ./
RUN bun install --frozen-lockfile
COPY management-ui/ ./
COPY VERSION /release-version
ARG VERSION
RUN VERSION="${VERSION:-v$(cat /release-version)}" bun --bun run build

FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /ui/dist/index.html ./internal/native/web/management.html
ARG VERSION
ARG COMMIT=none
ARG BUILD_DATE=unknown
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.Version=${VERSION:-v$(cat VERSION)} -X main.Commit=${COMMIT} -X main.BuildDate=${BUILD_DATE}" \
    -o /out/CLIProxyAPI ./cmd/server

FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata libstdc++6 \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /CLIProxyAPI
COPY --from=builder /out/CLIProxyAPI ./CLIProxyAPI
COPY config.docker.example.yaml config.example.yaml
COPY LICENSE /usr/share/licenses/cliproxyapi/LICENSE
COPY management-ui/LICENSE /usr/share/licenses/cliproxyapi/management-ui-LICENSE
ENV TZ=Asia/Shanghai
EXPOSE 8317
CMD ["./CLIProxyAPI"]
