# CLIProxyAPI Native

[中文部署教程](README_CN.md) · [Upstream](https://github.com/router-for-me/CLIProxyAPI) · [Releases](https://github.com/DesuwaDev/CLIProxyAPI/releases)

This fork includes usage/cost statistics, account and key controls, pricing, diagnostics, and optional Codex request controls in CPA's original management panel. One process, one panel, no sidecar.

## Docker Compose

Requires Docker Engine 27+ and Docker Compose v2 or later. The image supports native `amd64` and `arm64`.

```bash
mkdir -p cliproxyapi && cd cliproxyapi
curl -fL https://github.com/DesuwaDev/CLIProxyAPI/releases/download/v2026.9.1/docker-compose.yml -o docker-compose.yml
curl -fL https://github.com/DesuwaDev/CLIProxyAPI/releases/download/v2026.9.1/config.docker.example.yaml -o config.yaml
mkdir -p auths logs data plugins
```

Edit `config.yaml`: replace `remote-management.secret-key` and `api-keys` with your own long, distinct keys. Keep `native-management.enabled: true` to use the built-in modules. Then start:

Keep `host: ""` inside the container. Compose restricts the host port to `127.0.0.1:8317`; binding CPA itself to container loopback prevents Docker forwarding and causes Caddy 502 errors.

```bash
docker compose pull
docker compose up -d
docker compose logs --tail=100 -f
```

Configure Caddy on the host with your domain pointing to this server:

```caddyfile
cpa.example.com {
    reverse_proxy 127.0.0.1:8317
}
```

Replace the domain, then run `sudo caddy validate --config /etc/caddy/Caddyfile && sudo systemctl reload caddy`. A containerized Caddy should instead share CPA's Docker network and proxy to `cli-proxy-api:8317`.

- Management panel: `https://YOUR_DOMAIN/management.html` — use the management key.
- API base URL: `https://YOUR_DOMAIN/v1` — use a client API key.
- Upload existing OAuth account files or start OAuth login in the panel. Compose publishes all callback ports by default: Codex `1455`, Gemini `8085`, Claude `54545`, Antigravity `51121`, and iFlow `11451`. For remote deployments, a browser's localhost callback still needs forwarding to the server, or use the panel's callback submission option when available.
- The versioned image is `ghcr.io/desuwadev/cliproxyapi:v2026.9.1`; Docker selects the architecture automatically.

[Compose file](docker-compose.yml) · [Minimal Docker config](config.docker.example.yaml) · [Full config reference](config.example.yaml)

IPv6-only VPS hosts use Caddy for public access while its upstream remains `127.0.0.1:8317`. Container IPv6 egress and dual-stack OAuth callback ports are enabled by default. The host still needs working IPv6 DNS/connectivity to registries and upstream services; IPv4-only destinations need a proxy or NAT64/DNS64. Remove any old `CLI_PROXY_BIND=0.0.0.0` override to restore loopback-only API publishing. Set `CLI_PROXY_IPV6=false` only on hosts with IPv6 disabled.

Containers do not inherit the host's `/etc/hosts`. If the host uses custom IPv6 relay entries for GitHub, add the required mappings under `services.cli-proxy-api.extra_hosts` in a local `docker-compose.override.yml`, using your own relay addresses. Image pulls use the host's network configuration.

For an existing deployment, apply this network change with `docker compose down && docker compose up -d` after updating Compose. Data in the mounted directories is retained.

## Updates and persistent data

The deployment pins a release. Set `CLI_PROXY_IMAGE=ghcr.io/desuwadev/cliproxyapi:latest` in `.env` to follow stable releases, or specify another version, then run `docker compose pull && docker compose up -d`.

Keep `config.yaml`, `auths/`, `data/`, and any installed `plugins/`. The `data/` directory contains statistics, prices and module settings; do not omit its mount. Stop CPA before copying its SQLite database for a consistent backup. Host Caddy with a loopback-only API port is the default. For direct port access, set `CLI_PROXY_BIND=0.0.0.0` (IPv4) or `CLI_PROXY_BIND=[::]` (IPv6) in `.env`, then run `docker compose up -d`.

## Releases

Only `v20*` tags trigger image builds. Releases use `vYEAR.MONTH.PATCH`, starting at `v2026.9.1`; this is CPA's fork version, independent of the Codex client version. To publish: update `VERSION` and the pinned deployment examples, commit, tag `v$(cat VERSION)`, and push the commit and tag.

GitHub Actions builds both the current frontend and backend on native x86-64/ARM64 runners, checks the images, then publishes the multi-architecture version and `latest` to GHCR. The slim Debian runtime retains CGO/dynamic-plugin support; registry layer caches reduce repeat work. No QEMU or executable packer is used. A new fork may need its GHCR package visibility set to public for anonymous pulls.

Local source build: `docker build -t cliproxyapi-native .`.

## License

MIT. Based on [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) and its [management frontend](https://github.com/router-for-me/Cli-Proxy-API-Management-Center). Original copyright and license notices are retained.
