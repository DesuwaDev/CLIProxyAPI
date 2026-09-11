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

```bash
docker compose pull
docker compose up -d
docker compose logs --tail=100 -f
```

- Management panel: `http://YOUR_SERVER_IP:8317/management.html` — use the management key.
- API base URL: `http://YOUR_SERVER_IP:8317/v1` — use a client API key.
- Upload existing OAuth account files or start OAuth login in the panel. Compose publishes all callback ports by default: Codex `1455`, Gemini `8085`, Claude `54545`, Antigravity `51121`, and iFlow `11451`. For remote deployments, a browser's localhost callback still needs forwarding to the server, or use the panel's callback submission option when available.
- The versioned image is `ghcr.io/desuwadev/cliproxyapi:v2026.9.1`; Docker selects the architecture automatically.

[Compose file](docker-compose.yml) · [Minimal Docker config](config.docker.example.yaml) · [Full config reference](config.example.yaml)

For IPv6-only VPS hosts, Compose publishes both IPv4/IPv6 ports and enables container IPv6 egress by default. Use brackets in URLs: `http://[YOUR_IPV6_ADDRESS]:8317/management.html`. The host still needs working IPv6 DNS/connectivity to registries and upstream services; IPv4-only destinations need a proxy or NAT64/DNS64. Remove any old `CLI_PROXY_BIND=0.0.0.0` or `CLI_PROXY_OAUTH_BIND=0.0.0.0` overrides to restore dual-stack listeners. Set `CLI_PROXY_IPV6=false` only on hosts with IPv6 disabled.

Containers do not inherit the host's `/etc/hosts`. If the host uses custom IPv6 relay entries for GitHub, add the required mappings under `services.cli-proxy-api.extra_hosts` in a local `docker-compose.override.yml`, using your own relay addresses. Image pulls use the host's network configuration.

For an existing deployment, apply this network change with `docker compose down && docker compose up -d` after updating Compose. Data in the mounted directories is retained.

## Updates and persistent data

The deployment pins a release. Set `CLI_PROXY_IMAGE=ghcr.io/desuwadev/cliproxyapi:latest` in `.env` to follow stable releases, or specify another version, then run `docker compose pull && docker compose up -d`.

Keep `config.yaml`, `auths/`, `data/`, and any installed `plugins/`. The `data/` directory contains statistics, prices and module settings; do not omit its mount. Stop CPA before copying its SQLite database for a consistent backup. For a reverse proxy on the same host, set `CLI_PROXY_BIND=127.0.0.1` in `.env` and terminate HTTPS at the proxy.

## Releases

Only `v20*` tags trigger image builds. Releases use `vYEAR.MONTH.PATCH`, starting at `v2026.9.1`; this is CPA's fork version, independent of the Codex client version. To publish: update `VERSION` and the pinned deployment examples, commit, tag `v$(cat VERSION)`, and push the commit and tag.

GitHub Actions builds both the current frontend and backend on native x86-64/ARM64 runners, checks the images, then publishes the multi-architecture version and `latest` to GHCR. The slim Debian runtime retains CGO/dynamic-plugin support; registry layer caches reduce repeat work. No QEMU or executable packer is used. A new fork may need its GHCR package visibility set to public for anonymous pulls.

Local source build: `docker build -t cliproxyapi-native .`.

## License

MIT. Based on [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) and its [management frontend](https://github.com/router-for-me/Cli-Proxy-API-Management-Center). Original copyright and license notices are retained.
