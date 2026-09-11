# CLIProxyAPI 内置增强版

[English](README.md) · [上游项目](https://github.com/router-for-me/CLIProxyAPI) · [版本发布](https://github.com/DesuwaDev/CLIProxyAPI/releases)

在 CPA 原版面板中加入请求与费用统计、账号和 Key 管理、价格同步、请求诊断，以及可开关的 Codex 请求控制。一个进程、一个面板，不需要外挂面板。

## Docker Compose 部署

先安装 Docker Engine 27+ 和 Docker Compose v2 或更新版本。镜像支持 `amd64`、`arm64`，Docker 自动选择架构。

**1. 下载本版本的部署文件**

```bash
mkdir -p cliproxyapi && cd cliproxyapi
curl -fL https://github.com/DesuwaDev/CLIProxyAPI/releases/download/v2026.9.1/docker-compose.yml -o docker-compose.yml
curl -fL https://github.com/DesuwaDev/CLIProxyAPI/releases/download/v2026.9.1/config.docker.example.yaml -o config.yaml
mkdir -p auths logs data plugins
```

**2. 编辑 `config.yaml`，替换两处密钥**

- `remote-management.secret-key`：登录管理面板的密码。
- `api-keys`：提供给客户端调用的 Key。

使用不同的长随机密钥。保留 `native-management.enabled: true`，这样才会启用我们的内置面板和模块。

**容器内必须保留 `host: ""`。** 本机访问限制由 Compose 的 `127.0.0.1:8317:8317` 实现；把 `config.yaml` 的 `host` 改为 `127.0.0.1` 会导致 Docker 转发失败、Caddy 返回 502。

**3. 启动**

```bash
docker compose pull
docker compose up -d
docker compose logs --tail=100 -f
```

**4. 配置宿主机 Caddy 反代**

8317 默认只监听宿主机 `127.0.0.1`。将域名解析到服务器，在宿主机 Caddy 的配置中加入以下站点，并将域名替换为自己的：

```caddyfile
cpa.example.com {
    reverse_proxy 127.0.0.1:8317
}
```

校验并加载配置：`sudo caddy validate --config /etc/caddy/Caddyfile && sudo systemctl reload caddy`。打开 `https://你的域名/management.html`，用管理密钥登录；客户端 API 地址填写 `https://你的域名/v1`，使用 `api-keys` 中的 Key。Caddy 如果也在容器内，应通过同一 Docker 网络反代 `cli-proxy-api:8317`。

可在面板中上传已有 OAuth 账号文件，也可发起 OAuth 登录。Compose 默认开放全部回调端口：Codex `1455`、Gemini `8085`、Claude `54545`、Antigravity `51121`、iFlow `11451`，无需取消注释。远程服务器登录时，浏览器的 localhost 回调仍需通过端口转发到达服务器，或使用面板提供的手动提交回调功能。

镜像：`ghcr.io/desuwadev/cliproxyapi:v2026.9.1`。统计与管理默认可用，账号自动处置、请求审查和每账号指纹策略仍需明确开启。

**IPv6-only VPS：** 容器默认启用 IPv6 出网，OAuth 回调端口保持双栈开放；面板和 API 通过 Caddy 的域名访问，本机反代仍使用 `127.0.0.1:8317`。宿主机需能解析并通过 IPv6 访问镜像仓库和上游；仅提供 IPv4 的目标需要可用的代理或 NAT64/DNS64。删除旧 `.env` 中的 `CLI_PROXY_BIND=0.0.0.0` 可恢复仅本机监听。完全禁用 IPv6 的主机可设置 `CLI_PROXY_IPV6=false`。

如果宿主机依靠 `/etc/hosts` 中的 IPv6 转发地址访问 GitHub，容器不会继承这些映射。可在本地新建 `docker-compose.override.yml`，通过 `services.cli-proxy-api.extra_hosts` 补充容器需要的域名映射，例如 `api.github.com=你实际使用的IPv6转发地址`。镜像拉取则使用宿主机的网络配置。

已有部署切换到这个网络配置时，更新 Compose 后执行 `docker compose down && docker compose up -d`，重建网络即可；上述目录中的数据会保留。

[直接查看 Compose](docker-compose.yml) · [精简部署配置](config.docker.example.yaml) · [完整配置参考](config.example.yaml)

## 更新与数据保存

默认固定版本。要跟随稳定版，在同目录 `.env` 写入：

```dotenv
CLI_PROXY_IMAGE=ghcr.io/desuwadev/cliproxyapi:latest
```

也可以把 `latest` 换成指定版本。然后执行：

```bash
docker compose pull
docker compose up -d
```

保留 `config.yaml`、`auths/`、`data/` 和已安装插件所在的 `plugins/`。**`data/` 保存统计、价格和模块设置，不要漏挂载。** 备份 SQLite 数据前先 `docker compose stop`，复制完再 `docker compose start`。

默认由同机 Caddy 提供 HTTPS。确需直接暴露 8317 时，可在 `.env` 设置 `CLI_PROXY_BIND=0.0.0.0`（IPv4）或 `CLI_PROXY_BIND=[::]`（IPv6），然后执行 `docker compose up -d`。

## 自行发布

仅推送 `v20*` tag 才构建镜像，普通 commit 不发布。版本使用 `v年.月.修订号`，首版为 `v2026.9.1`；它是本项目版本，与面板里配置的 Codex CLI 版本无关。

发布时同步修改根目录 `VERSION`、Compose 和教程中的固定版本，提交后执行：

```bash
git tag "v$(cat VERSION)"
git push origin main
git push origin "v$(cat VERSION)"
```

Actions 会从当前源码重新构建面板并嵌入后端；amd64 使用 x86-64 runner，arm64 使用 ARM64 runner，分别检查后发布到 GHCR。最终运行镜像使用 Debian slim，保留 CGO 和动态插件支持，使用仓库构建缓存，不使用 QEMU 模拟或可执行文件压缩。Fork 到新仓库首次发布时，如需免登录拉取，在 GitHub Packages 中把镜像可见性设为 Public。

本地构建：`docker build -t cliproxyapi-native .`。

## 开源说明

基于 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 与[原版管理面板](https://github.com/router-for-me/Cli-Proxy-API-Management-Center)，保留原作者版权与 MIT 许可证。
