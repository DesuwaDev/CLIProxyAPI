# CLIProxyAPI Native

[English](README.md) · [中文部署教程](README_CN.md) · [リリース](https://github.com/DesuwaDev/CLIProxyAPI/releases)

CPA の管理画面に統計、料金計算、アカウント・API Key 管理、診断、任意の Codex リクエスト設定を組み込んだ派生版です。別の管理サービスは不要です。

## Docker Compose

Docker Engine と Docker Compose v2 を用意してください。amd64 / arm64 に対応しています。

```bash
mkdir -p cliproxyapi && cd cliproxyapi
curl -fL https://raw.githubusercontent.com/DesuwaDev/CLIProxyAPI/v2026.9.1/docker-compose.yml -o docker-compose.yml
curl -fL https://raw.githubusercontent.com/DesuwaDev/CLIProxyAPI/v2026.9.1/config.docker.example.yaml -o config.yaml
mkdir -p auths logs data plugins
```

`config.yaml` の管理用 `remote-management.secret-key` とクライアント用 `api-keys` を、異なる長い秘密鍵に変更してください。`native-management.enabled: true` を維持してください。

```bash
docker compose pull
docker compose up -d
```

管理画面は `http://SERVER_IP:8317/management.html`、API は `http://SERVER_IP:8317/v1` です。既存の OAuth ファイルは管理画面からアップロードできます。OAuth ログインにコールバックが必要な場合は、Compose にコメントされている対応ポートを有効にし、ブラウザーから到達できるようにしてください。

イメージは `ghcr.io/desuwadev/cliproxyapi:v2026.9.1` です。[Compose](docker-compose.yml) と[設定例](config.docker.example.yaml)を参照してください。`config.yaml`、`auths/`、統計と設定を保存する `data/`、導入済み `plugins/` を保持してください。

更新は `.env` の `CLI_PROXY_IMAGE` を希望するタグに設定し、`docker compose pull && docker compose up -d` を実行します。`latest` は安定版を追跡します。

バージョンは `v年.月.修訂番号`、タグの push 時だけ Actions がネイティブ x86-64 / ARM64 ビルドを実行します。元の著作権表示と MIT ライセンスを保持しています。[上流プロジェクト](https://github.com/router-for-me/CLIProxyAPI)。
