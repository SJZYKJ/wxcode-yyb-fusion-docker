# wxcode-YYB

**微信协议网关 + 多账号控制台（Go）**

手机微信扫码授权 · 多渠道凭据 · 小程序 `wx.login` code 获取 · 凭据续期与保活 · 账号编队 · 青龙 / 呆呆 / Arcadia 联动 · Docker 部署

<img src="docs/images/banner.svg" alt="wxcode-YYB" width="100%">

## 这是什么

wxcode-YYB 是一个自托管服务：用手机微信扫码把账号授权进来，服务保存凭据并在后台续期，然后对内提供两类能力——

1. **协议取码**：按账号调用 `wx.login` 拿到小程序 code（`POST /wxapp/getCode`），也支持公众号网页授权取 code（`/wx/h5code`），以及一批 `/wx/*` 兼容入口（用户信息、手机号、云函数、加密密钥等）。
2. **多账号控制台**：账号工作台、扫码渠道与多渠道账号、账号编队、独立代理、运行日志、掉线通知、面板（青龙 / 呆呆 / Arcadia）对接与按账号运行脚本。

服务同时兼容 `GET /login?appId=` 这条旧的 wxcode 线格式取码请求（`internal/httpapi/fusion.go`），便于已有调用方直接迁移，不必改脚本。

## 扫码渠道

控制台的扫码页、渠道选择器和账号卡片都从同一份渠道清单（`internal/qr/channels.go`）生成，目前有 5 条：

| 渠道 ID | 名称 | 说明 |
| --- | --- | --- |
| `appstore` | 应用宝 | 默认渠道 |
| `pcmgr` | 腾讯电脑管家 | 纯 Go 实现握手与取票 |
| `gameassist` | 腾讯手游助手 | 需要对应客户端登录态 |
| `ldplayer` | 雷电模拟器 | 实验性 |
| `qbbrowser` | QQ 浏览器 | 独立扫码，配套 `extensions/qbbrowser-token/` 扩展 |

同一个微信号可以用不同渠道各扫一次，再合并成一个**多渠道账号**；多个渠道账号还能绑定成**编队（Fleet）**，取码时在成员之间轮转和故障转移。渠道清单可通过 `GET /api/scan-channels` 读取。

## 核心能力

- **账号与工作台**：扫码添加 / 重复扫码更新、备注、OpenID 查看、状态刷新、合账、账号 ID 压缩、按账号删除。
- **取码与防护**：取码熔断、同账号请求限速、登录预算、网站应用 AppID 拒取与空码临时限制（`YYB_CIRCUIT_BREAKER`、`YYB_CODE_MIN_GAP`、`YYB_LOGIN_BUDGET` 等，见下文变量表）。
- **保活与代理**：后台按凭据剩余时间刷新；每个账号可独立配置直连 / 静态代理 / SOCKS5 / 动态代理 API。
- **面板联动**：青龙、呆呆、Arcadia 三套面板驱动，同步 `YYB_SERVER`，按账号创建、启停、运行任务并读取隔离日志。
- **脚本管理**：`scripts/` 下的脚本可用 `tools/yyb-scriptctl.sh` 整库同步、单脚本拉取，或拉取并创建青龙任务。
- **用户与权限**：独立登录注册、管理员与普通用户、账号归属、脚本目录可见范围。
- **可观测性**：`eventlog` 运行日志与 `/runs` 视图、`GET /api/system-logs`、掉线通知（Server酱 / PushPlus / 企业微信机器人）。

## 快速开始（Docker Compose）

需要 Docker、Docker Compose v2，以及一个供面板互通的外部网络（默认名 `qinglong_default`）。

```bash
cp .env.example .env      # 融合部署可用 .env.fusion.example
docker compose pull yyb-go
docker compose up -d
```

浏览器打开 `http://<服务器地址>:<YYB_PORT>`。未预设管理员（`YYB_ADMIN_USER` / `YYB_ADMIN_PASSWORD`）时，第一个注册用户自动成为管理员。容器自带健康检查：`GET /health`。

**自建镜像**（`compose.yaml` 本身带 `build` 段，构建阶段会先跑 `go test ./...`）：

```bash
docker compose build            # 只构建镜像
docker compose up -d --build    # 构建并启动
```

默认镜像名为 `chungg/wxcode-yyb-fusion:latest`，可用 `YYB_IMAGE` 覆盖。

**数据与网络**：`compose.yaml` 把 `./data/db`、`./data/avatars`、`./data/qr` 挂到容器内 `/app/resource/{db,avatars,qr}`；容器使用的外部网络由 `YYB_DOCKER_NETWORK` 决定（默认 `qinglong_default`）。首次启动前请确认映射目录存在且容器用户可写。

### 端口

| 端口 | 含义 | 出处 |
| --- | --- | --- |
| `8000` | 容器内服务端口（`-port 8000`） | `compose.yaml`、`Dockerfile`（`EXPOSE 8000`） |
| `${YYB_PORT:-8000}` | 宿主机映射端口，默认 **8000** | `compose.yaml` 的 `ports` |
| `8088` | 融合部署示例里给网关留的宿主机端口 | `.env.fusion.example` 的 `YYB_PORT=8088` |
| `8080` | 可选的 Nginx 反代端口（转发到 `yyb-go:8000`） | `nginx.conf` + `Dockerfile.web` |

## 环境变量

配置优先级：**显式命令行参数 > 已有进程环境 > 程序旁的 `.env` > 默认值**。配置只在启动时读取，改完要重启（Docker 通常要重新创建容器）。

### 服务与镜像

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `YYB_PORT` | `8000` | 宿主机映射端口（`compose.yaml`）；原生运行时是服务端口 |
| `YYB_BIND_ADDRESS` | `0.0.0.0` | 监听地址 |
| `YYB_IMAGE` | `chungg/wxcode-yyb-fusion:latest` | Compose 使用的镜像 |
| `YYB_DOCKER_NETWORK` | `qinglong_default` | 容器加入的外部网络名 |

### 登录、用户与令牌

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `YYB_AUTH_DRIVER` | `sqlite` | 控制台登录库：`sqlite` / `mysql` / `none` |
| `YYB_AUTH_DSN` | 空 | 对应驱动的连接串 |
| `YYB_AUTH_MYSQL_DSN` | 空 | 旧版 MySQL 变量，设置后自动切到 mysql |
| `YYB_ADMIN_USER` / `YYB_ADMIN_PASSWORD` | 空 | 预设管理员，仅在用户库为空时生效 |
| `YYB_COOKIE_SECURE` | `false` | HTTPS 反代时设为 `true` |
| `YYB_PROTOCOL_TOKEN` | 空 | 保护 `/wx/*` 与 `/wxapp/*`，公网使用必须设置 |
| `YYB_API_TOKEN` | 空 | 公开接口令牌；未配置时启动会自动生成并写入数据库（启动日志可见） |
| `YYB_INTEGRATION_TOKEN` | 空 | `/integration/*` 集成接口令牌 |
| `YYB_ACCOUNT_LINK_KEY` | 空 | 授权短链的加密密钥，正式使用请设置并保持不变 |

### 取码、防护与保活

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `YYB_KEEPALIVE_INTERVAL` / `YYB_KEEPALIVE_AHEAD` | `2m` / `4m` | 保活检查间隔与提前刷新时间（`.env.fusion.example` 用 `30m` / `45m`） |
| `YYB_DEVICE_IDENTITY` | `stable` | 每个账号固定一套协议设备；`random` 恢复旧行为 |
| `YYB_APPSTORE_REFRESH_MODE` | `legacy_auth` | 应用宝渠道刷新策略 |
| `YYB_CIRCUIT_BREAKER` | `on` | 取码熔断开关 |
| `YYB_CODE_MIN_GAP` / `YYB_CODE_MAX_WAIT` | `3s` / `20s` | 同账号向上游请求的最小间隔与最长排队时间 |
| `YYB_LOGIN_BUDGET` / `YYB_LOGIN_WINDOW` | `3` / `10m` | 登录预算：窗口内重新登录次数上限 |
| `YYB_FRESH_SESSION_WINDOW` | `2m` | 刚取到 code 后这段时间内的 `-14` 不重新登录 |
| `YYB_CODE_ALLOW_APPIDS` / `YYB_CODE_DENY_APPIDS` | 空 | 网站应用 AppID 的放行 / 追加禁用名单 |
| `YYB_CODE_DYNAMIC_DENY` / `YYB_CODE_DYNAMIC_DENY_THRESHOLD` | `1` / `2` | 空码临时限制的开关与阈值 |
| `YYB_DNS_SERVERS` | 空 | 自定义 DNS（Magisk / 特殊网络用） |
| `YYB_NOTIFY_CHANNEL` / `YYB_NOTIFY_TOKEN` / `YYB_NOTIFY_TOPIC` / `YYB_NOTIFY_ACCOUNT_PUSH` | 空 / 空 / 空 / `true` | 掉线通知渠道（`serverchan`、`pushplus`、`qywx`）与账号级推送开关 |

### 面板与其它

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PANEL_TYPE` | `qinglong` | `qinglong` / `daidai` / `arcadia` |
| `QL_URL` / `QL_CLIENT_ID` / `QL_CLIENT_SECRET` | 空 | 青龙连接信息 |
| `DAIDAI_URL` / `DAIDAI_APP_KEY` / `DAIDAI_APP_SECRET` | 空 | 呆呆连接信息 |
| `ARCADIA_URL` / `ARCADIA_TOKEN` | 空 | Arcadia 连接信息 |
| `YYB_QINGLONG_SERVER` | 按监听端口推导 | 写进面板 `YYB_SERVER` 的回连地址 `yyb-go:<端口>`；Docker 镜像为 `yyb-go:8088` |
| `YYB_QINGLONG_REPO` | `SuperNaiBA_YYB-GO-Script,qinglong/scripts` | 账号运行管理读取的脚本目录，逗号分隔 |
| `YYB_QINGLONG_REF_MODE` | `id` | `YYB_SERVER` 里的账号引用方式：`id` 或 `openid` |
| `YYB_ENABLE_PC_LOGIN` | `false` | 桌面微信快速授权（尚不稳定） |

源码里还有几个高风险开关（默认不启用、不在 `.env.example` 里）：`YYB_ALLOW_NO_AUTH`（关闭鉴权）、`YYB_ALLOW_REGISTRATION`（开放公开注册）、`YYB_TRUST_PROXY`（信任 `X-Forwarded-For`）。完整说明见[配置与账号安全](docs/configuration.md)。

## 协议接口

`YYB_SERVER` 每行一个账号，格式为 `http://<地址>@<账号 id 或 openid>`：

```text
http://yyb-go:8088@1
```

取小程序 code：

```bash
curl -X POST http://yyb-go:8088/wxapp/getCode \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer <令牌>' \
  -d '{"ref":"1","app_id":"wx0000000000000000"}'
```

- 配置了 `YYB_PROTOCOL_TOKEN` 或 `YYB_API_TOKEN` 时，请求需带 `Authorization: Bearer <令牌>`。
- 运行中的服务在 `/docs` 提供 OpenAPI 页面，`/openapi.json` 是机器可读定义。
- `GET /login?appId=wx...` 走兼容的 wxcode 线格式，可带 `ref` 指定账号。
- 公众号网页授权取 code 用 `POST /wx/h5code`（或 `/wxapp/getH5Code`）。
- 接口清单、OAuth 边界与参数说明见[协议接口说明](docs/protocol-api.md)。

## 面板联动与脚本

控制台里填连接信息，或直接写进 `.env`：

```dotenv
PANEL_TYPE=qinglong
QL_URL=http://qinglong:5700
QL_CLIENT_ID=你的 Client ID
QL_CLIENT_SECRET=你的 Client Secret
YYB_QINGLONG_SERVER=yyb-go:8088
YYB_QINGLONG_REPO=qinglong/scripts
```

青龙脚本拉取工具装在青龙容器内：

```bash
curl -fsSL https://raw.githubusercontent.com/SJZYKJ/wxcode-yyb-fusion-docker/main/src/tools/yyb-scriptctl.sh \
  -o /ql/data/scripts/yyb-scriptctl.sh
chmod +x /ql/data/scripts/yyb-scriptctl.sh

bash /ql/data/scripts/yyb-scriptctl.sh sync                 # 同步整个 scripts/ 目录
bash /ql/data/scripts/yyb-scriptctl.sh pull 麦富迪_code版.py  # 只拉一个脚本
bash /ql/data/scripts/yyb-scriptctl.sh install 麦富迪_code版.py --cron "13 1 * * *" --name "麦富迪"
```

目录、任务名和去重规则见[脚本管理](scripts/README.md#青龙脚本拉取工具)。

## 目录结构

```text
cmd/yyb-go/          主程序（启动、参数、环境变量、管理员密码重置）
cmd/protocol-lab/    协议实验用命令行工具
internal/            业务实现：auth 登录与归属、store 数据、protocol 协议、qr 扫码渠道、
                     pcmgr / gameassist / ldplayer / qbbrowser 各客户端渠道、
                     httpapi HTTP 路由与控制台接口、eventlog 运行日志、proxysource 代理来源
resource/            内嵌资源：控制台模板、静态文件与默认数据库结构
docs/                部署、配置、协议、面板与各类调查记录
scripts/             青龙脚本与配套工具
packaging/           Magisk 模块（Android 安装包与运行脚本）
tools/               脚本同步、发布说明、协议检查等运维工具
extensions/          QQ 浏览器取票伴生扩展（qbbrowser-token）
tests/               控制台 UI 与脚本的端到端检查
```

## 文档索引

| 文档 | 内容 |
| --- | --- |
| [文档索引](docs/README.md) | 全部文档按用途分组 |
| [配置与账号安全](docs/configuration.md) | 配置优先级、用户库、管理员、Cookie 与 HTTPS、令牌、数据目录与备份 |
| [Docker 部署与排错](docs/docker-deployment.md) | 镜像与固定版本、网络名、SQLite 权限、源码构建失败 |
| [控制台与账号管理](docs/console-features.md) | 账号工作台、扫码渠道、多渠道账号、合并账号、接口测试、运行日志 |
| [面板接入与排错](docs/panel-integration.md) | 青龙、呆呆、Arcadia、跨服务器访问、运行管理与日志 |
| [协议接口说明](docs/protocol-api.md) | `/wxapp/*`、`/wx/*`、`wx.login` code、`operateWxData`、公众号 OAuth 与能力边界 |
| [代理与账号保活](docs/proxy-keepalive.md) | 静态 / 动态代理、刷新策略、失效排查 |
| [授权稳定性](docs/authorization-stability.md) | 已有防护措施、它们不能保证什么、验证边界 |
| [授权与缓存会话分层](docs/credential-session-layers.md) | OAuth 拒绝与协议会话为什么要分开处理 |
| [脚本管理](scripts/README.md) | 脚本清单、安装、任务元数据与拉取工具 |
| [Magisk 模块](docs/magisk.md) | Android ARM64 安装、升级、局域网监听与 DNS |
| [构建与发布](docs/release-builds.md) | Docker、Magisk、多架构二进制与发布流程 |
| [更新日志](CHANGELOG.md) | 本项目的版本记录 |

文档目录里带日期的文件是当时的调查或复核记录，写的是当时的结论和验证边界。

## 安全边界

- 默认只应部署在可信局域网、VPN 或受控反向代理之后，不要把协议接口和数据库直接暴露到公网。
- 公网使用必须配 HTTPS、`YYB_COOKIE_SECURE=true` 和随机长令牌（`YYB_PROTOCOL_TOKEN` / `YYB_API_TOKEN`）。
- 不要提交 `.env`、`data/`、SQLite 数据库、代理密钥、OpenID 或微信登录凭据。
- `wx.login` code 短期且一次性；公众号 OAuth code 必须由用户在微信内完成授权后回调产生。
- 本项目不伪造微信返回值，也不从缺失的业务参数推导签名、加密数据或授权结果。
- 授权可能被上游拒绝而需要重新扫码，现有的熔断、限速与登录预算只降低请求频率，不保证授权长期有效。
