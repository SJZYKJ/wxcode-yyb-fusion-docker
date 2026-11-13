# wxcode-YYB 融合网关

**微信协议网关 + 多账号控制台**，单个 Docker 容器即可运行。

手机微信扫码授权 → 服务保存并续期凭据 → 对内提供「按账号取小程序 code」与「多账号控制台」两类能力。
自带 Web 管理界面、渠道化扫码、账号合并与编队、掉线与保活通知，并与青龙 / 呆呆 / Arcadia 面板联动。

```text
镜像：chungg/wxcode-yyb-fusion:latest
端口：宿主 8088 → 容器 8088
健康检查：GET /health
```
**界面截图**：

<img width="1910" height="883" alt="扫码" src="https://github.com/user-attachments/assets/3608b2f5-3fa7-402c-90b1-6646c99ca0f1" />
<img width="1910" height="883" alt="五合一" src="https://github.com/user-attachments/assets/83da6cf5-9970-42cd-abb7-88867d6ea2bd" />
<img width="1910" height="883" alt="五分五" src="https://github.com/user-attachments/assets/37d6b5aa-6581-4d95-94df-b9dcadb41c69" />

---

## 目录

- [项目定位](#项目定位)
- [功能总览](#功能总览)
- [多渠道取码功能](#多渠道取码功能)
- [部署方式](#部署方式)
- [镜像拉取](#镜像拉取)
- [自行构建与推送](#自行构建与推送)
- [配置说明](#配置说明)
- [对接其他渠道（青龙 / 自建脚本 / 其他面板）](#对接其他渠道青龙--自建脚本--其他面板)
- [接口示例](#接口示例)
- [数据、备份与升级](#数据备份与升级)
- [目录结构](#目录结构)
- [文档索引](#文档索引)
- [安全边界](#安全边界)
- [常见问题](#常见问题)
- [版本摘要](#版本摘要)

---

## 项目定位

很多自动化脚本需要小程序或公众号的 `code`（`wx.login` / 网页授权）。手工取码无法持续，直接用账号密码登录又有风险。

本项目的做法是：**用手机微信扫码把账号授权一次**，服务端保存协议凭据并在后台自动续期，之后脚本只要带上账号引用请求网关，就能随时拿到可用的 `code`——扫码在浏览器里完成，凭据不出服务器。

配套的控制台把「有哪些账号、各自多久过期、跑在哪个面板、什么时候掉线」放在一张页面上，并提供账号级隔离、脚本目录可见范围与访问令牌等管理能力。

## 功能总览

| 能力 | 说明 |
| --- | --- |
| 多渠道扫码 | 应用宝、腾讯电脑管家、腾讯手游助手、雷电模拟器、QQ 浏览器 5 条渠道，同一微信号可多渠道绑定后合并 |
| 取码 | 小程序 `wx.login` code、公众号网页授权 code、以及 `/wx/*` 兼容接口（用户信息、手机号、云函数、加密密钥等） |
| 账号管理 | 扫码添加 / 重复扫码刷新、备注、状态与有效期、合账、账号 ID 压缩、按账号删除 |
| 保活 | 后台按凭据剩余时间自动刷新，可配置检查间隔与提前量 |
| 代理 | 每账号独立直连 / 静态代理 / SOCKS5 / 动态代理 API |
| 面板联动 | 青龙、呆呆、Arcadia 三套面板驱动：同步 `YYB_SERVER`、按账号建任务、启停、读隔离日志 |
| 脚本管理 | 内置脚本集合与同步工具，可按账号运行、按目录放行可见范围 |
| 用户与权限 | 独立登录注册、管理员与普通用户、账号归属、「脚本可读」开关 |
| 可观测性 | 运行日志与调用记录视图、`GET /api/system-logs`、掉线通知（Server酱 / PushPlus / 企业微信机器人） |
| 防封 | 取码熔断、同账号限速、登录预算、网站应用 AppID 拒取与空码临时限制 |

## 多渠道取码功能

扫码页、渠道选择器和账号卡片都由同一份渠道清单生成（`internal/qr/channels.go`）：

| 渠道 ID | 名称 | 说明 |
| --- | --- | --- |
| `appstore` | 应用宝 | 默认渠道 |
| `pcmgr` | 腾讯电脑管家 | 纯 Go 实现握手与取票，无需安装客户端 |
| `gameassist` | 腾讯手游助手 | 需要对应客户端登录态 |
| `ldplayer` | 雷电模拟器 | 实验性 |
| `qbbrowser` | QQ 浏览器 | 独立扫码，配套 `extensions/qbbrowser-token/` 扩展 |

同一个微信号可以用不同渠道各扫一次，再合并成一个**多渠道账号**；多个渠道账号还能绑定成**编队（Fleet）**，取码时在成员之间轮转与故障转移。渠道清单可通过 `GET /api/scan-channels` 读取。

## 部署方式

前置条件：只要 Docker（方式二一条 `docker run` 即可）；要用 compose 需要 Docker Compose v2。如果要和面板互通，需要两者共用一个 Docker 网络（默认名 `qinglong_default`）。

### 方式一：一键脚本（最省事，脚本会代做下面两种方式的所有步骤）

```bash
curl -fsSL https://raw.githubusercontent.com/SJZYKJ/wxcode-yyb-fusion-docker/main/deploy.sh | bash
```

脚本会准备目录与 `.env`、生成 48 位访问令牌、拉取镜像、启动容器并做健康检查，最后打印访问地址与令牌。
重复执行等于「升级重启」，不会覆盖已有数据与令牌。

常用参数：

| 参数 | 作用 |
| --- | --- |
| `--dir <路径>` | 部署目录（默认 `./wxcode-yyb-fusion-docker`） |
| `--port <端口>` | 宿主机端口（默认 8088） |
| `--bind <地址>` | 监听地址（默认 `0.0.0.0`；公网机建议 `127.0.0.1` 再套反代） |
| `--token <令牌>` | 指定访问令牌（默认随机生成） |
| `--image-tag <标签>` | 指定镜像标签，默认 `latest`；回滚时填 `v17` 这类发布号 |
| `--build` | 用本仓库源码本地构建镜像 |
| `--dry-run` | 只生成 `.env` 并打印命令，不启动容器 |
| `--uninstall` | 停止并删除容器（保留数据目录） |

### 方式二：docker run（推荐首选：一行拆成几行，数据放宿主机）

镜像已发布在 Docker Hub，不需要 clone 仓库；数据放在宿主机，重建容器不丢账号。

**Linux / macOS：**

```bash
docker run -d --name wxcode-yyb-gateway --restart unless-stopped \
  -p 8088:8088 -e TZ=Asia/Shanghai \
  -v "$PWD/data/db:/app/resource/db" \
  -v "$PWD/data/avatars:/app/resource/avatars" \
  -v "$PWD/data/qr:/app/resource/qr" \
  chungg/wxcode-yyb-fusion:v17
```

**Windows PowerShell：**

```powershell
docker run -d --name wxcode-yyb-gateway --restart unless-stopped `
  -p 8088:8088 -e TZ=Asia/Shanghai `
  -v "${PWD}\data\db:/app/resource/db" `
  -v "${PWD}\data\avatars:/app/resource/avatars" `
  -v "${PWD}\data\qr:/app/resource/qr" `
  chungg/wxcode-yyb-fusion:v17
```

- 三个目录必须**分别**挂载（`db` / `avatars` / `qr`），不要把整个 `/app/resource` 挂上去——那会盖掉镜像里的页面模板和静态资源。
- **`YYB_API_TOKEN` 留空就行**：首次启动会自动生成一个随机令牌、存进数据库（重启不变），并把令牌打印在容器日志里。查看：

  ```bash
  docker logs wxcode-yyb-gateway 2>&1 | grep 令牌
  ```

  想固定（例如脚本里已经写死了令牌），加 `-e YYB_API_TOKEN=<你的令牌>` 即可，脚本侧 `YYB_API_KEY` 填同一个值。
- 只想临时试一下（数据随容器一起删，不挂卷）：

  ```bash
  docker run -d --name wxcode-yyb-gateway --restart unless-stopped -p 8088:8088 chungg/wxcode-yyb-fusion:latest
  ```

- 升级：`docker pull chungg/wxcode-yyb-fusion:latest` → `docker rm -f wxcode-yyb-gateway` → 再执行上面同一条 `docker run`（卷不动，数据保留）。

### 方式三：docker compose 拉取镜像（变量多、长期维护时用）

```bash
git clone https://github.com/SJZYKJ/wxcode-yyb-fusion-docker.git
cd wxcode-yyb-fusion-docker

cp .env.example .env        # 默认即可；YYB_API_TOKEN 留空会自动生成并打印到日志
docker compose pull
docker compose up -d
```

### 方式四：自己构建镜像（可选，一般不需要）

```bash
# A. 完全在 Docker 内构建（构建阶段会先跑 go test ./...）
docker compose -f compose.build.yaml up -d --build

# B. 先在本机交叉编译，再只打镜像（Go 工具链自备）
./build-gateway.sh                       # 产出 gateway/yyb-go-amd64 与 arm64
docker compose -f compose.build.yaml build
docker compose -f compose.build.yaml up -d
```

### 启动后

浏览器打开 `http://<服务器地址>:8088`：

1. **首次启动会自动创建管理员账号**：用户名 `admin`，密码是容器启动时随机生成的 16 位串，打印在日志里 —— `docker logs <容器名> 2>&1 | grep 管理员`。登录后到「个人设置 → 修改密码」改掉。想固定账号密码就设 `YYB_ADMIN_USER` / `YYB_ADMIN_PASSWORD`；也可以用内网注册的第一个账号当管理员（需要已配置 SMTP 并打开注册开关）。
2. 进入「添加账号」扫码授权，回到「工作台」即可看到账号与有效期。
3. 在「个人设置」里配置面板（青龙等）与通知渠道，再到「运行与日志」按账号建任务。

## 镜像拉取

```bash
docker pull chungg/wxcode-yyb-fusion:latest    # 跟随最新构建
docker pull chungg/wxcode-yyb-fusion:v17       # 固定版本，便于回滚
```

- 版本号是全局递增的整数发布号（`v17`、`v18`…），构建期写入镜像内的 `/static/version.json`，登录页与 `/api/version` 显示的都是它。
- 固定版本：在 `.env` 里设 `IMAGE_TAG=v17`，或部署脚本加 `--image-tag v17`。

## 配置说明

配置优先级：**命令行参数 > 进程环境 > 程序旁的 `.env` > 内置默认值**。配置只在启动时读取，改完要重新创建容器。

### 服务与镜像

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `IMAGE_REPO` | `chungg/wxcode-yyb-fusion` | Compose 使用的镜像仓库 |
| `IMAGE_TAG` | `latest` | 镜像标签，回滚时填发布号 |
| `YYB_PORT` | `8088` | 宿主机映射端口 |
| `YYB_BIND_ADDRESS` | `0.0.0.0` | 监听地址 |
| `DATA_DIR` | `./data` | 数据目录（SQLite / 头像 / 二维码） |
| `YYB_DOCKER_NETWORK` | `qinglong_default` | 容器加入的外部网络 |
| `TZ` | `Asia/Shanghai` | 容器时区（镜像已内置 tzdata） |

### 登录、用户与令牌

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `YYB_AUTH_DRIVER` | `sqlite` | 控制台登录库：`sqlite` / `mysql` / `none` |
| `YYB_AUTH_DSN` | 空 | 对应驱动的连接串 |
| `YYB_ADMIN_USER` / `YYB_ADMIN_PASSWORD` | `admin` / 自动生成 | 预设管理员，仅在用户库为空时生效（密码 ≥10 位）；**密码留空时首次启动自动生成随机密码并打印到容器日志**，不配 SMTP 也能登录 |
| `YYB_COOKIE_SECURE` | `false` | HTTPS 反代时设为 `true` |
| `YYB_API_TOKEN` | 空 | 取码接口令牌；留空时启动自动生成并落库，日志可见 |
| `YYB_ALLOW_NO_AUTH` | `false` | 显式关闭取码接口鉴权（⚠️ 端口可达即可取码） |
| `YYB_ALLOW_REGISTRATION` | `false` | 公开注册开关 |
| `YYB_TRUST_PROXY` | `false` | 只有确实在反向代理后面才开 |
| `YYB_PROTOCOL_TOKEN` | 空 | 保护 `/wx/*` 与 `/wxapp/*` 的协议令牌 |
| `YYB_INTEGRATION_TOKEN` | 空 | `/integration/*` 集成接口令牌 |
| `YYB_SMTP_*` | 空 | 注册邮箱验证码的 SMTP 配置，留空则注册被明确拒绝 |

### 取码、防护与保活

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `YYB_KEEPALIVE_INTERVAL` / `YYB_KEEPALIVE_AHEAD` | `30m` / `45m` | 保活检查间隔与提前刷新时间 |
| `YYB_CIRCUIT_BREAKER` | `on` | 取码熔断开关 |
| `YYB_CODE_MIN_GAP` / `YYB_CODE_MAX_WAIT` | `3s` / `20s` | 同账号最小请求间隔与最长排队时间 |
| `YYB_LOGIN_BUDGET` / `YYB_LOGIN_WINDOW` | `3` / `10m` | 登录预算：窗口内重新登录次数上限 |
| `YYB_CODE_ALLOW_APPIDS` / `YYB_CODE_DENY_APPIDS` | 空 | 网站应用 AppID 放行 / 追加禁用 |
| `YYB_DEVICE_IDENTITY` | `stable` | 每个账号固定一套协议设备 |
| `YYB_NOTIFY_CHANNEL` / `YYB_NOTIFY_TOKEN` / `YYB_NOTIFY_TOPIC` | 空 | 掉线通知渠道（`serverchan` / `pushplus` / `qywx`） |

### 面板

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PANEL_TYPE` | `qinglong` | `qinglong` / `daidai` / `arcadia` |
| `QL_URL` / `QL_CLIENT_ID` / `QL_CLIENT_SECRET` | 空 | 青龙连接信息 |
| `DAIDAI_URL` / `DAIDAI_APP_KEY` / `DAIDAI_APP_SECRET` | 空 | 呆呆连接信息 |
| `ARCADIA_URL` / `ARCADIA_TOKEN` | 空 | Arcadia 连接信息 |
| `YYB_QINGLONG_SERVER` | 空（按监听端口推导） | 写进面板 `YYB_SERVER` 的回连地址 `<host>:<port>`；留空时由容器监听端口推导，本镜像为 `yyb-go:8088`。跨机部署必须填实际 IP 或域名 |
| `YYB_QINGLONG_REPO` | `qinglong/scripts` | 账号运行管理读取的脚本目录，逗号分隔可写多个 |
| `YYB_QINGLONG_REF_MODE` | `id` | `YYB_SERVER` 里的账号引用方式：`id` 或 `openid` |

### 设备、协议与通知补充

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `YYB_PLATFORM` | `linux/amd64` | 镜像架构；ARM 机器写 `linux/arm64` |
| `YYB_DNS_SERVERS` | `223.5.5.5:53,119.29.29.29:53` | 容器内解析用的 DNS，家宽/机房解析异常时可换 |
| `YYB_ENABLE_PC_LOGIN` | `false` | 桌面微信快速授权（需要在本机装微信，容器里一般用不到） |
| `YYB_APPSTORE_REFRESH_MODE` | `legacy_auth` | 应用宝渠道刷新模式；改成 `source` 走另一条刷新链路 |
| `YYB_NOTIFY_ACCOUNT_PUSH` | `true` | 有脚本推送渠道的账号掉线时也走该渠道通知 |
| `YYB_SMTP_TLS` | `true` | SMTP 是否走 TLS（465 端口保持 `true`，25/587 明文按服务商要求） |
| `YYB_FRESH_SESSION_WINDOW` | 空 | 复用「新鲜会话」的时间窗，留空用内置值 |
| `YYB_CODE_DYNAMIC_DENY` / `YYB_CODE_DYNAMIC_DENY_THRESHOLD` | 空 | 学习型动态拒绝开关与阈值，一般用控制台「协议熔断总控」调，不必写进 `.env` |

> 上面四张表加上这张表，覆盖 `.env.example` 里的全部键；改完可执行 `docker compose config` 核对最终生效值。

> 脚本目录：把本仓库的脚本集合放到青龙脚本根目录下的 `qinglong/scripts`（或把 `YYB_QINGLONG_REPO` 改成你实际使用的目录名）。
> 目录更名后，旧目录下的青龙任务不会自动迁移，需要按新目录重建一次。

## 对接其他渠道（青龙 / 自建脚本 / 其他面板）

网关只负责「取码」，业务脚本跑在哪边都行。分两半配置：**网关侧**决定谁能取码、面板怎么连；**脚本侧**决定去哪儿取码。

### 网关侧要配的变量

| 变量 | 什么时候需要 | 作用 |
| --- | --- | --- |
| `YYB_API_TOKEN` | 必填（留空会启动时随机生成并打印到日志） | 脚本取码用的访问令牌，就是脚本里的 `YYB_API_KEY` |
| `PANEL_TYPE` | 用面板时 | `qinglong` / `daidai` / `arcadia` |
| `QL_URL` / `QL_CLIENT_ID` / `QL_CLIENT_SECRET` | 青龙 | 网关反过来访问青龙，用来同步账号变量与任务 |
| `YYB_QINGLONG_SERVER` | 建议显式写 | 青龙脚本回连网关的地址 `<host>:<port>`，默认按端口推导为 `yyb-go:8088`；跨机部署必须填实际 IP 或域名 |
| `YYB_QINGLONG_REPO` | 建议写 | 账号运行管理读取的脚本目录，默认 `qinglong/scripts`，可逗号分隔多个 |
| `YYB_QINGLONG_REF_MODE` | 可选 | `YYB_SERVER` 里的账号写法：`id`（默认，账号不会变）或 `openid` |
| `YYB_INTEGRATION_TOKEN` | 自建脚本/面板走 `/integration/*` | 集成接口令牌 |
| `YYB_PROTOCOL_TOKEN` | 可选 | 保护 `/wx/*`、`/wxapp/*` 协议路由的第二道令牌 |
| `YYB_DOCKER_NETWORK` | 与青龙同宿主机 | 两边加入同一个外部网络（默认 `qinglong_default`），容器名才能互访 |

### 脚本侧（青龙环境变量）要配的变量

青龙「环境变量」里只需要两个：

```text
YYB_SERVER=http://wxcode-yyb-gateway:8088@1
YYB_API_KEY=<网关的 YYB_API_TOKEN>
```

- **`YYB_SERVER`**：每行一个账号，格式 `http://<网关地址>@<账号 id 或 openid>`，多账号就多行。`id` 是控制台账号卡片上的编号；把 `YYB_QINGLONG_REF_MODE` 设成 `openid` 时这里写 openid。容器间互访用服务名 `yyb-go` 或容器名 `wxcode-yyb-gateway`；跨机部署写 `http://192.168.x.x:8088`。
- **`YYB_API_KEY`**：脚本会以 `Authorization: Bearer <值>` 发出去，值填网关的 `YYB_API_TOKEN`。网关三种带法都认：`Authorization: Bearer`、`X-API-Token`、`?token=`。
- 在控制台点「同步账号」后，网关会自动维护青龙里那条 `YYB_SERVER`，不需要手抄。
- 可选：`YYB_ACCOUNT_STATUS_FILE`（账号状态缓存文件）、`YYB_GUARD_BYPASS`（跳过脚本自带的守卫检查）。

青龙里建任务用脚本名即可，例如 `task qinglong/scripts/xxx.py`；账号由网关在任务里注入 `WECHAT_OPENIDS`，脚本里不要写死账号。

### 自建服务 / 其他面板：`/integration/*`

带 `Authorization: Bearer <YYB_INTEGRATION_TOKEN>`，接口是稳定契约：

| 接口 | 作用 |
| --- | --- |
| `POST /integration/actions/get-code` | 取码，body `{"ref":"1","app_id":"wx..."}`，返回 `{"account_id":1,"result":{...}}` |
| `POST /integration/actions/refresh-account` | 立即刷新指定账号的协议存活状态，body `{"ref":"1"}` |
| `GET /integration/accounts` | 账号列表（含状态、备注、脱敏 openid） |
| `POST /integration/accounts/proxy` | 给账号设置代理 |
| `GET /integration/module-manifest.json` | 能力清单（面板据此渲染页面与表单，`schema_version: 1`） |

## 接口示例

`YYB_SERVER` 每行一个账号，格式 `http://<地址>@<账号 id 或 openid>`：

```text
http://yyb-go:8088@1
```

取小程序 code：

```bash
curl -X POST http://127.0.0.1:8088/wxapp/getCode \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer <令牌>' \
  -d '{"ref":"1","app_id":"wx0000000000000000"}'
```

```json
{"source":"native","fallback":false,"openid":"...","result":{"code":"...","status":"success"}}
```

- 兼容旧调用格式：`GET /login?appId=wx...`（可带 `ref` 指定账号），返回 `err/msg/appId/status/code`。
- 公众号网页授权：`POST /wx/h5code`（或 `/wxapp/getH5Code`）。
- 运行中的服务在 `/docs` 提供接口页面，`/openapi.json` 是机器可读定义。
- 令牌可用三种方式携带：`Authorization: Bearer <令牌>`、`X-API-Token: <令牌>`、`?token=<令牌>`。

## 数据、备份与升级

- 数据都在 `DATA_DIR`（默认 `./data`）：`db/`（SQLite 库）、`avatars/`（头像）、`qr/`（二维码）。
- 备份 = 停容器后拷走整个 `data/` 目录；恢复 = 放回同一路径再启动。
- 升级：`docker compose pull && docker compose up -d`（或重跑 `deploy.sh`）。数据目录与令牌都保留。
- 回滚：`.env` 里把 `IMAGE_TAG` 固定成旧发布号，例如 `v16`，再 `up -d`。

## 目录结构

```text
.
├── compose.yaml                 单服务部署（拉取预构建镜像）
├── compose.build.yaml           源码本地构建
├── Dockerfile                   预编译二进制镜像
├── Dockerfile.src               源码构建镜像
├── build-gateway.sh             交叉编译 gateway/yyb-go-{amd64,arm64}
├── deploy.sh                    一键部署脚本
├── gateway/                     预编译二进制与内嵌资源（模板 / 静态文件）
├── src/                         Go 源码树
│   ├── cmd/yyb-go/              主程序
│   ├── internal/                auth / store / protocol / qr / httpapi / eventlog 等
│   ├── resource/                控制台模板、静态文件、默认库结构
│   ├── scripts/                 青龙脚本与同步工具
│   ├── docs/                    部署、配置、协议、面板与调查记录
│   ├── extensions/              QQ 浏览器取票伴生扩展
│   ├── packaging/               Magisk 模块（Android 打包，可选）
│   └── tests/                   控制台 UI 端到端检查
└── docs/                        镜像与部署补充文档
```

## 文档索引

| 文档 | 内容 |
| --- | --- |
| [纯 Docker 部署说明](README-DOCKER.md) | 取码原理、目录结构、手动部署、令牌与会话的可见范围 |
| [Docker 部署与排错](src/docs/docker-deployment.md) | 镜像与固定版本、网络名、SQLite 权限、源码构建失败 |
| [配置与账号安全](src/docs/configuration.md) | 配置优先级、用户库、管理员、Cookie 与 HTTPS、数据备份 |
| [控制台与账号管理](src/docs/console-features.md) | 工作台、扫码渠道、多渠道账号、合账、接口测试、运行日志 |
| [面板接入与排错](src/docs/panel-integration.md) | 青龙、呆呆、Arcadia、跨服务器访问、任务与日志 |
| [协议接口说明](src/docs/protocol-api.md) | `/wxapp/*`、`/wx/*`、`wx.login` code、`operateWxData`、OAuth 边界 |
| [代理与账号保活](src/docs/proxy-keepalive.md) | 静态 / 动态代理、刷新策略、失效排查 |
| [授权稳定性](src/docs/authorization-stability.md) | 已有防护、它们不能保证什么、如何验证 |
| [脚本管理](src/scripts/README.md) | 脚本目录约定、同步工具、按账号运行规则 |

## 安全边界

- **取码接口默认 fail-closed**：`/login` 取码分支、`/instances`、`/wxapp/*`、`/wx/*`、`/openapi.json` 需要令牌；`/health` 与 `/static` 始终开放，控制台走会话。
- **令牌不是会话**：令牌只能读「公开 API」范围的数据；账号可见性由归属与「脚本可读」开关决定。
- **公开注册默认关闭**：用户库为空时只允许内网来源完成首次注册，避免实例刚暴露就被抢注管理员。
- **容器权限收敛**：`no-new-privileges` + 仅保留挂载目录所需能力，进程以非 root 用户运行。
- 本项目只做协议自动化，不代理、不存储聊天内容；请只在**你有权管理的账号**上使用，并自行承担平台风控策略变化带来的风险。

## 常见问题

**取码返回 401 / `unauthorized`？** 没带令牌，或令牌与控制台不一致。令牌在启动日志里（「已自动生成 API 访问令牌」），也可在控制台重置。

**扫码后账号一直「待确认」？** 通常是渠道不匹配或授权被中途取消：换一个渠道重扫，或在「账号」里点「刷新状态」。

**青龙里看不到脚本？** 确认 `YYB_QINGLONG_REPO` 与实际脚本目录一致（默认 `qinglong/scripts`），并在「运行与日志」点「刷新状态」；面板任务需要用 `task <目录>/<脚本>` 形式。

**定时任务跑了全部账号？** 手动运行只跑选中账号；打开「定时」后按网关登录账号隔离。任务命令里的 `WECHAT_OPENIDS` 不要写死，交给网关注入。

**升级后登录页版本没变？** 镜像标签可能还停在旧发布号，检查 `.env` 的 `IMAGE_TAG` 与 `docker compose images`。

## 版本摘要
- **v23** **同步版本号，为其他更新**：最新版还是V17.
- **v17** **多个取码渠道 + 界面重构**：取码渠道从 1 条（应用宝）扩到 5 条 —— 新增腾讯电脑管家、腾讯手游助手、雷电模拟器（实验）、QQ 浏览器，渠道清单由服务端统一下发，扫码页、账号卡片、设置页共用同一份；同一微信号多渠道取码可合并，账号新增「扫码渠道」标识与账号矩阵主账号。控制台重做外壳与页面：横幅 + 分组侧栏 + 底部工具栏，一级入口改为「工作台 / 我的微信账号 / 添加账号 / 运行与日志 / 代理设置」，管理分组为「用户管理 / 运行日志」，上线账号矩阵、账号级代理与代理档案、已激活短链接、系统日志、多账号一次性短链接页，支持明暗主题。新增取码熔断 / 限速 / 登录预算与 AppID 黑白名单（页面可在线调，环境变量只作默认值）、H5 取码链路、设备标识、桌面微信快速授权（默认关）、协议令牌与集成令牌、掉线通知、青龙脚本管理。配置变量 16 → 40 项，HTTP 接口 57 → 95 条，新增 `/api/version` 与 `/static/version.json`。完整差异见 [CHANGELOG](src/CHANGELOG.md)。 本次补充（v17 重新发布）：**零配置部署不再卡在注册** —— 未配 SMTP 时注册页用不了，首次启动会自动创建管理员 `admin` 与 16 位随机密码并打印到容器日志（`docker logs <容器名> 2>&1 | grep 管理员`），账号名可用 `YYB_ADMIN_USER` 覆盖；注册页补上第四个功能卡「多渠道取码」并加未配 SMTP 的说明；代理设置里的微信账号列表不再显示头像（改为首字母色块，避免遮挡名字）；个人设置新增「接口令牌」卡片，可查看 / 复制 / 重新生成 `YYB_API_KEY`（即 `YYB_API_TOKEN`），重新生成后立即生效、旧令牌立刻失效，令牌由环境变量固定时界面只读。
- **v16** **注册邮箱验证 + 用户批量删除**：注册页新增邮箱字段与邮箱验证码（6 位、10 分钟有效、一次性消费、同邮箱发送限流；服务端未配置 SMTP 时直接拒绝注册，绝不静默放行）；邮箱作为唯一索引，重复注册被拒。用户管理页新增行复选框、表头全选与「删除所选」按钮，可勾选多名成员一次性删除，删除前二次确认，并在同一事务内保护当前登录账号与最后一名管理员不被误删。登录页版本号固定显示 v16（镜像内 `version.json` 优先，读不到时回退常量，不再出现占位符）。
- **v15** 修复青龙新建文件夹内的脚本在网关看不到的问题：青龙里新建的脚本目录（文件夹）现在能被网关照常识别，目录内的脚本会正确出现在「账号运行管理」列表中。
- **v13** **账号隔离真正落地 + 界面整体焕新 + 安卓支持下线**：修复「点运行一个账号却跑了全部」—— 三个取码脚本与 `code脚本/` 公共模块按任务前命令里的 `WECHAT_OPENIDS` 过滤账号，**手动运行只跑选中的那一个账号、定时只跑登录账号名下的账号**，白名单无匹配账号时空跑结束、绝不回退跑全部。安卓手机端（wxcode APK / Xposed Hook / Zygisk）取码支持与相关接口（`/wxcode/*`、`/whoami`、`/wxapp/deviceCode`）整体移除，只保留 Docker 部署与原生扫码协议。登录页与功能页视觉全面重做（靛蓝新主题），普通用户界面不再出现任何管理入口，用户管理页仅管理员可访问。
- **v10** 账号隔离拆成两档：**「运行」只跑你选中的那一个 code 账号**，**「定时」按网关登录账号隔离 —— 跑该登录账号名下的全部 code 账号**（同一脚本只建一条定时任务，不会重复执行）。另外新增「脚本目录可见范围」：管理员可以指定非管理员账号只能看到青龙里哪些文件夹下的脚本，看不到的脚本既不会列出、也不能运行或开定时。老版本遗留的「每个 code 账号一条、还启用着」的定时任务，会在打开「账号运行管理」时自动收敛成一条登录账号级任务（只停旧的、不删）。
- **v9** 修复「运行账号」越界：在网关上点运行，脚本不再去枚举网关里的**全部**微信账号，而是**只跑所选的那一个** —— 网关创建的账号任务会把任务前命令里的 `WECHAT_OPENIDS` 覆写成本账号（此前只写了 `YYB_SERVER`，而脚本池里的脚本并不读它，所以点一次运行会把所有账号都跑一遍）。旧的托管任务在你下次点「运行」或拨动定时开关时会自动带上这个限定。**在青龙里直接运行同一个脚本不受影响**，仍按青龙的全局 `WECHAT_OPENIDS` 取账号（写 `ALL` 即全部账号）。
- **v8** 「账号运行管理」的脚本列表改为**读青龙脚本目录里的脚本文件**，不再从定时任务反推 —— 定时任务回答的是「要跑什么」，脚本文件才是「能跑什么」，混在一起会让用户自己建的任务被当成可用脚本，同一个脚本还会被全局任务和账号任务各跑一遍。列表上方可按目录筛选，在青龙里已有全局任务的脚本会被标注出来提醒重复执行；面板没有脚本接口时（老青龙、代代面板）自动退回原来的推断方式并在页面上注明。
- **v7** 「账号运行管理」的脚本列表改为直接读青龙里的**全部定时任务**，不再要求任务命令的目录事先命中 `YYB_QINGLONG_REPO`（那个配置项没有界面、文档里也没写，漏配就一个脚本都列不出来）。列表为空时也会分清是「青龙里确实没有任务」还是「有任务但命令认不出脚本」。
- **v6** 修复横向越权：登录控制台后，普通账号用浏览器访问 `/instances` 曾能列出**全部**账号的 openid，并可借 `api_shared` 默认开启按 `ref` 取到他人（含管理员）的 code。现按凭据类型区分权限模型 —— **令牌**＝公开 API（不做归属过滤，脚本行为不变），**会话**＝控制台（普通用户只能读写自己的账号）；`/wxcode/*` 三条设备侧接口收窄为「令牌或管理员」。
- **v5** 安全加固：取码接口改为 fail-closed（令牌未配置自动生成并落库），`/wxcode/*` 设备 hook 入口纳入鉴权，注册默认关闭 + 首个管理员仅限内网注册，登录限速改为账号/IP 双通道且默认不信任 `X-Forwarded-For`，修复 `next` 参数的开放重定向。
- **v4** 公开接口可选访问令牌（`YYB_API_TOKEN`）；部署编排拆分为 `compose.yaml`（拉镜像）/ `compose.build.yaml`（本地构建）；新增 GitHub Actions 双架构发布流水线；新增一键部署脚本 `deploy.sh`。
- **v3** 多用户账号隔离：普通用户只看自己扫码添加的账号，账号级「脚本可读」开关控制是否对脚本暴露。
- **v2** 修复 bind-mount 数据目录权限导致的 SQLite 打不开；`/login` 无设备时自动回退原生微信登录。

## 免责声明

本项目仅供个人学习与自用自动化，不提供任何形式的担保。使用前请确认符合你所在地区的法律法规与平台服务条款；因使用本项目产生的账号风险与法律责任由使用者自行承担。
