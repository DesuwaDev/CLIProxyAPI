# CLIProxyAPI Native

[English](README.md) · [中文部署教程](README_CN.md) · [リリース](https://github.com/DesuwaDev/CLIProxyAPI/releases)

CPA の管理画面に統計、料金計算、アカウント・API Key 管理、診断、任意の Codex リクエスト設定を組み込んだ派生版です。別の管理サービスは不要です。

## Docker Compose

Docker Engine 27+ と Docker Compose v2 以降を用意してください。amd64 / arm64 に対応しています。

```bash
mkdir -p cliproxyapi && cd cliproxyapi
curl -fL https://github.com/DesuwaDev/CLIProxyAPI/releases/download/v2026.9.1/docker-compose.yml -o docker-compose.yml
curl -fL https://github.com/DesuwaDev/CLIProxyAPI/releases/download/v2026.9.1/config.docker.example.yaml -o config.yaml
mkdir -p auths logs data plugins
```

`config.yaml` の管理用 `remote-management.secret-key` とクライアント用 `api-keys` を、異なる長い秘密鍵に変更してください。`native-management.enabled: true` を維持してください。

コンテナー内の `host: ""` は変更しないでください。ホスト側の公開先を Compose で `127.0.0.1:8317` に制限します。コンテナー内で `host: "127.0.0.1"` を指定すると、Docker 転送が失敗し Caddy が 502 を返します。

```bash
docker compose pull
docker compose up -d
```

ドメインをサーバーに向け、ホストの Caddy に次の設定を追加してください：

```caddyfile
cpa.example.com {
    reverse_proxy 127.0.0.1:8317
}
```

ドメインを変更し、`sudo caddy validate --config /etc/caddy/Caddyfile && sudo systemctl reload caddy` を実行します。管理画面は `https://YOUR_DOMAIN/management.html`、API は `https://YOUR_DOMAIN/v1` です。Caddy もコンテナーの場合は同じ Docker ネットワーク上の `cli-proxy-api:8317` に転送します。

既存の OAuth ファイルをアップロードするか、管理画面から OAuth ログインを開始できます。コールバックポートはすべて初期状態で公開されます：Codex `1455`、Gemini `8085`、Claude `54545`、Antigravity `51121`、iFlow `11451`。リモート環境では、ブラウザーの localhost コールバックをサーバーへ転送するか、管理画面のコールバック手動送信機能を使用してください。

イメージは `ghcr.io/desuwadev/cliproxyapi:v2026.9.1` です。[Compose](docker-compose.yml) と[設定例](config.docker.example.yaml)を参照してください。`config.yaml`、`auths/`、統計と設定を保存する `data/`、導入済み `plugins/` を保持してください。

更新は `.env` の `CLI_PROXY_IMAGE` を希望するタグに設定し、`docker compose pull && docker compose up -d` を実行します。`latest` は安定版を追跡します。

IPv6-only VPS でも公開アクセスは Caddy 経由、転送先は `127.0.0.1:8317` です。コンテナーの IPv6 通信と OAuth コールバックの IPv4／IPv6 公開は有効です。ホスト側でもレジストリと上流への IPv6 接続と名前解決が必要です。IPv4 のみの宛先にはプロキシまたは NAT64/DNS64 が必要です。以前の `.env` の `CLI_PROXY_BIND=0.0.0.0` を削除すると API はローカル公開に戻ります。IPv6 を無効にしたホストでは `CLI_PROXY_IPV6=false` を設定できます。

コンテナーはホストの `/etc/hosts` を継承しません。GitHub 用の IPv6 中継アドレスを設定している場合、ローカルの `docker-compose.override.yml` の `services.cli-proxy-api.extra_hosts` に必要な対応を追加してください。イメージの取得にはホストのネットワーク設定が使われます。

既存環境のネットワーク設定を切り替える場合、Compose 更新後に `docker compose down && docker compose up -d` を実行してください。マウントしたディレクトリーのデータは保持されます。

バージョンは `v年.月.修訂番号`、タグの push 時だけ Actions がネイティブ x86-64 / ARM64 ビルドを実行します。元の著作権表示と MIT ライセンスを保持しています。[上流プロジェクト](https://github.com/router-for-me/CLIProxyAPI)。
