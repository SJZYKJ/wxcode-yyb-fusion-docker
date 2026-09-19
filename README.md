# wxcode + YYB Go 融合网关

**单容器跑通微信小程序取码**：浏览器扫码登录 → 青龙脚本取 code。无需 root 手机、无需 Android 容器、无需 Xposed。

[![Docker Pulls](https://img.shields.io/docker/pulls/chungg/wxcode-yyb-fusion)](https://hub.docker.com/r/chungg/wxcode-yyb-fusion)
[![Image Size](https://img.shields.io/docker/image-size/chungg/wxcode-yyb-fusion/latest)](https://hub.docker.com/r/chungg/wxcode-yyb-fusion/tags)
[![平台](https://img.shields.io/badge/platform-amd64%20%7C%20arm64-blue)](#)
[![构建](https://github.com/SJZYKJ/wxcode-yyb-fusion-docker/actions/workflows/docker-publish.yml/badge.svg)](https://github.com/SJZYKJ/wxcode-yyb-fusion-docker/actions/workflows/docker-publish.yml)

---

## ⚡ 一键部署

### 方式一 · 一条命令（推荐，不用 clone）

```bash
curl -fsSL https://raw.githubusercontent.com/SJZYKJ/wxcode-yyb-fusion-docker/main/deploy.sh | bash
```

脚本自动完成：**检查 Docker → 下载编排文件 → 生成随机访问令牌并写入 `.env` → 建数据目录 → 拉镜像 → 启动 → 健康检查 → 打印访问地址与令牌**。

### 方式二 · clone 后执行

```bash
git clone https://github.com/SJZYKJ/wxcode-yyb-fusion-docker.git
cd wxcode-yyb-fusion-docker
./deploy.sh
```

### 方式三 · 纯 docker compose（三行）

```bash
git clone https://github.com/SJZYKJ/wxcode-yyb-fusion-docker.git && cd wxcode-yyb-fusion-docker
cp .env.example .env
docker compose up -d
```

> 不用手填令牌也不会裸奔：网关首次启动会**自动生成访问令牌**、存进数据库（跟随数据卷持久化，重启不变）并打印到 `docker compose logs | grep 令牌`。

### deploy.sh 常用参数

```bash
./deploy.sh --help
```

| 参数 | 说明 |
|---|---|
| `--port 8088` | 宿主机端口 |
| `--bind 127.0.0.1` | 只监听本机（配合 Nginx 反代更安全） |
| `--token <令牌>` | 指定访问令牌（默认自动生成 48 位随机串） |
| `--no-token` | 关闭访问令牌（⚠️ 取码接口裸奔，仅限完全可信内网） |
| `--allow-registration` | 开放公开注册（默认关闭） |
| `--trust-proxy` | 信任 `X-Forwarded-For`（确实有反代时才开） |
| `--image-tag v5` | 固定版本 / 回滚（`v<N>` 为全局递增的正式发布号） |
| `--build` | 用本仓库源码本地构建（改 Go 代码后用） |
| `--dry-run` | 只准备 `.env` 并打印命令，不启动容器 |
| `--logs` | 启动后跟踪容器日志 |
| `--uninstall` | 停止并删除容器（**数据保留**） |

> 脚本是**幂等**的：重复执行 = 拉新镜像 + 重启，不会覆盖你已有的 `.env`、访问令牌和数据。
> Windows 上可用 Git Bash / WSL 执行同一个脚本（已处理 MSYS 路径差异）。

---

## ✅ 部署完成后做三件事

1. 浏览器打开 `http://<宿主IP>:8088/` → 首次部署时**从内网**访问 `/register` 注册第一个账号（自动成为管理员）。
   已有账号则直接 `/login` 登录。注册默认关闭，且**公网来源会被拒绝**（防止实例刚上线就被陌生人抢注管理员）——
   若你只能从公网初始化，请在 `.env` 里预先设置 `YYB_ADMIN_USER` / `YYB_ADMIN_PASSWORD` 再重启。
2. 打开 `http://<宿主IP>:8088/scan` → **手机微信扫码**，把微信号添加进来；
3. 青龙里给脚本配上同名令牌：环境变量 `YYB_API_TOKEN` = 部署时打印的那个值
   （顺丰中秋 / sfsy日常版 / 移动云盘 三个脚本已内置支持，**两边值必须一致**）。

### 常用地址

| 地址 | 用途 |
|---|---|
| `http://<host>:8088/` | Web 面板（账号管理、运行管理） |
| `http://<host>:8088/scan` | 微信扫码登录页 |
| `http://<host>:8088/instances` | 账号实例列表（脚本枚举用，需令牌） |
| `http://<host>:8088/wxapp/getCode` | 指定账号取码（脚本用，需令牌） |
| `http://<host>:8088/health` | 健康检查（始终免鉴权） |

---

## 🔐 安全（默认即安全）

| 项目 | 默认行为 |
|---|---|
| 取码类接口<br>`/login` 取码分支、`/instances`、`/whoami`、`/wxapp/*`、`/wx/*`、`/wxcode/*`、`/openapi.json` | **必须带访问令牌**。`YYB_API_TOKEN` 留空时网关自动生成随机令牌、落库持久化并打印到容器日志，**不存在无鉴权的窗口**。 |
| 关闭鉴权 | 只有显式设 `YYB_ALLOW_NO_AUTH=true` 才会生效（启动日志会有醒目警告）。 |
| 公开注册 | **默认关闭**。库中无账号时仅允许内网/回环地址完成首个管理员注册；公网直连一律拒绝。要公开注册须显式 `YYB_ALLOW_REGISTRATION=true`。 |
| 登录限速 | 按「账号」+「来源 IP」双通道计数（15 分钟窗口）。默认**不信任** `X-Forwarded-For`；确实有反代时才设 `YYB_TRUST_PROXY=true`，否则攻击者可伪造该头绕过限速。 |
| 会话 Cookie | `HttpOnly` + `SameSite=Lax`；HTTPS 请求（直连或 `X-Forwarded-Proto: https`）自动加 `Secure`，也可用 `YYB_COOKIE_SECURE=true` 强制。 |
| 开放重定向 | 登录 `next` 参数只接受站内路径，`//host`、`/\host` 与控制字符注入一律收敛到 `/`。 |
| 容器 | 非 root 运行（`yyb` 用户）、`cap_drop: ALL`、`no-new-privileges`。 |

脚本侧三种带令牌方式任选：`Authorization: Bearer <t>` / `X-API-Token: <t>` / `?token=<t>`
（推荐前两种，`?token=` 会出现在访问日志与 Referer 里）。

> ⚠️ 数据卷 `.env` 里 `DATA_DIR` 指向的目录含微信登录凭据（SQLite 明文），请勿放到可被外部访问的共享目录。

---

## 🧰 日常运维

| 操作 | 命令 |
|---|---|
| 看日志 | `docker compose logs -f --tail=100` |
| 升级 | `./deploy.sh`（自动 `pull` + 重启） |
| 停服 | `./deploy.sh --uninstall` |
| 改端口 | 编辑 `.env` 的 `YYB_PORT`，再跑 `./deploy.sh` |
| 备份数据 | 拷走 `.env` 里 `DATA_DIR` 指向的目录（默认 `./data`） |
| 本地构建 | `./build-gateway.sh && docker compose -f compose.build.yaml up -d --build` |

---

## 🧩 它是什么

| 通道 | 原理 | 依赖 | 默认 |
|---|---|---|---|
| **原生扫码登录** | 浏览器 `/scan` 出二维码 → 手机微信扫码 → 登录态自动保存并刷新 | 无（纯 Docker） | ✅ 开启 |
| wxcode 设备 | 已 root 手机装 wxcode APK，由 Xposed hook 微信进程取码 | root 手机 + LSPosed | 可选 |

yyb-go 原生扫码已覆盖全部取码需求，**裸 Docker 就能跑**；wxcode 只是给有条件的人多一个设备端点（设备 hook 是 Android 组件，无法编译进 Linux 的 Go 服务，见 [详细文档](README-DOCKER.md) 第一节）。

---

## 📚 更多文档

| 文档 | 内容 |
|---|---|
| [README-DOCKER.md](README-DOCKER.md) | 完整部署文档：目录结构、取码接口、多用户账号隔离（v4.2.2）、访问令牌细节（v4.2.3）、wxcode 设备接入、FAQ |

**版本摘要**

- **v9** 修复「运行账号」越界：在网关上点运行，脚本不再去枚举网关里的**全部**微信账号，而是**只跑所选的那一个** —— 网关创建的账号任务会把任务前命令里的 `WECHAT_OPENIDS` 覆写成本账号（此前只写了 `YYB_SERVER`，而脚本池里的脚本并不读它，所以点一次运行会把所有账号都跑一遍）。旧的托管任务在你下次点「运行」或拨动定时开关时会自动带上这个限定。**在青龙里直接运行同一个脚本不受影响**，仍按青龙的全局 `WECHAT_OPENIDS` 取账号（写 `ALL` 即全部账号）。
- **v8** 「账号运行管理」的脚本列表改为**读青龙脚本目录里的脚本文件**，不再从定时任务反推 —— 定时任务回答的是「要跑什么」，脚本文件才是「能跑什么」，混在一起会让用户自己建的任务被当成可用脚本，同一个脚本还会被全局任务和账号任务各跑一遍。列表上方可按目录筛选，在青龙里已有全局任务的脚本会被标注出来提醒重复执行；面板没有脚本接口时（老青龙、代代面板）自动退回原来的推断方式并在页面上注明。
- **v7.1** 「账号运行管理」的脚本列表改为直接读青龙里的**全部定时任务**，不再要求任务命令的目录事先命中 `YYB_QINGLONG_REPO`（那个配置项没有界面、文档里也没写，漏配就一个脚本都列不出来）。列表为空时也会分清是「青龙里确实没有任务」还是「有任务但命令认不出脚本」。
- **v6** 修复横向越权：登录控制台后，普通账号用浏览器访问 `/instances` 曾能列出**全部**账号的 openid，并可借 `api_shared` 默认开启按 `ref` 取到他人（含管理员）的 code。现按凭据类型区分权限模型 —— **令牌**＝公开 API（不做归属过滤，脚本行为不变），**会话**＝控制台（普通用户只能读写自己的账号）；`/wxcode/*` 三条设备侧接口收窄为「令牌或管理员」。
- **v5** 安全加固：取码接口改为 fail-closed（令牌未配置自动生成并落库），`/wxcode/*` 设备 hook 入口纳入鉴权，注册默认关闭 + 首个管理员仅限内网注册，登录限速改为账号/IP 双通道且默认不信任 `X-Forwarded-For`，修复 `next` 参数的开放重定向。
- **v4** 公开接口可选访问令牌（`YYB_API_TOKEN`）；部署编排拆分为 `compose.yaml`（拉镜像）/ `compose.build.yaml`（本地构建）；新增 GitHub Actions 双架构发布流水线；新增一键部署脚本 `deploy.sh`。
- **v3** 多用户账号隔离：普通用户只看自己扫码添加的账号，账号级「脚本可读」开关控制是否对脚本暴露。
- **v2** 修复 bind-mount 数据目录权限导致的 SQLite 打不开；`/login` 无设备时自动回退原生微信登录。
