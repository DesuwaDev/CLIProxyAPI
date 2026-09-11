#!/usr/bin/env bash
set -euo pipefail
work="$(mktemp -d)"
name="cpa-release-check"
cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT
cp config.docker.example.yaml "$work/config.yaml"
management_key="$(openssl rand -hex 24)"
sed -i "s/replace-with-a-long-management-key/$management_key/" "$work/config.yaml"
mkdir -p "$work/data" "$work/auths"
CLI_PROXY_CONFIG_PATH="$work/config.yaml" docker compose config --quiet
docker run -d --name "$name" -p 127.0.0.1:18317:8317 \
  -v "$work/config.yaml:/CLIProxyAPI/config.yaml" \
  -v "$work/data:/CLIProxyAPI/data" \
  -v "$work/auths:/root/.cli-proxy-api" \
  "$IMAGE" ./CLIProxyAPI -config /CLIProxyAPI/config.yaml --local-model >/dev/null
for attempt in $(seq 1 45); do
  if curl -fsS http://127.0.0.1:18317/management.html -o "$work/panel.html" 2>/dev/null; then break; fi
  sleep 1
done
curl -fsS -D "$work/headers" -H "Authorization: Bearer $management_key" \
  http://127.0.0.1:18317/v0/management/native/status -o "$work/status.json" || { docker logs "$name"; exit 1; }
jq -e '.modules | map(.name) | contains(["history","pricing","inventory","headers","wire","fingerprint","risk"])' "$work/status.json"
grep -Fq "$RELEASE_VERSION" "$work/panel.html"
grep -Fq 'codex-disguise' "$work/panel.html"
grep -Fiq "$RELEASE_VERSION" "$work/headers"
test -s "$work/data/native-management.sqlite"
docker image inspect "$IMAGE" --format 'Architecture: {{.Architecture}}; unpacked bytes: {{.Size}}'
echo 'Embedded UI, release version, native modules and persistent database verified.'
