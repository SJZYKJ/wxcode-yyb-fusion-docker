# wxcode + YYB Go 融合网关 — 纯 Docker 部署

> 把 wxcode 2.1.0 的"微信小程序取码"能力与 YYB Go 网关融合，**核心能力全部由 Docker 单服务提供**：
> 微信扫码登录（浏览器 /scan 出二维码 → 手机微信扫码）本来就是 YYB Go 的原生能力，无需 root、
> 无需 Xposed、无需 redroid；wxcode 模块（Xposed 注入微信进程）降级为**可选的外部设备取码端点**。

## ⚡ 一键部署（推荐）

```bash
# 不用 clone：自动下载编排文件、生成访问令牌、启动并做健康检查
curl -fsSL https://raw.githubusercontent.com/SJZYKJ/wxcode-yyb-fusion-docker/main/deploy.sh | bash

# 或者 clone 后在仓库里执行
git clone https://github.com/SJZYKJ/wxcode-yyb-fusion-docker.git
cd wxcode-yyb-fusion-docker && ./deploy.sh
```

脚本幂等：重复执行 = 拉新镜像 + 重启，不会覆盖已有的 `.env`、令牌与数据。
`./deploy.sh --help` 查看全部参数（端口、绑定地址、指定令牌、镜像标签、`--dry-run`、
`--logs`、`--uninstall`）；默认会**生成并开启访问令牌**（说明见第五节）。

不想用脚本，就照第三节的三行 compose 手动部署。

## 一、本质结论（为什么这么设计）

从 wxcode_2.1.0.apk 解码源码（wxcode_java/）可以确认：

- wxcode 是一个 **Xposed 模块**（入口 `WxLoginHook implements IXposedHookLoadPackage`），
  必须被注入到微信进程（com.tencent.mm）内部运行；
- 它 hook 微信的 `Application.attach` 与 `JsApiLogin$LoginTask`，反射构造登录任务，
  在微信进程内启动 `NanoHTTPD` 监听 8088（分身实例 +100 偏移），对外提供
  `GET /login?appId=`、`/instances`、`/register` 等接口；
- 因此 wxcode 是**纯 Java/Android 组件，无法编译进 Linux 上的 Go 服务**；
  它的"实时取码"能力，yyb-go 原生扫码登录链路（/scan → 手机扫码 → login_buffer）
  已经覆盖，且更轻（不需要 root 手机）。

融合方式 = **yyb-go 为主（Docker 原生扫码登录）+ wxcode 为可选设备通道**：

| 通道 | 原理 | 依赖 | 默认 |
|---|---|---|---|
| 原生扫码登录 | 浏览器 /scan 出码，手机微信扫码，login_buffer 自动刷新 | 无（纯 Docker） | ✅ 开启 |
| wxcode 设备 | 已 root 手机装 wxcode APK，NanoHTTPD :8088 取码 | root 手机 + LSPosed | 可选（WXCODE_URLS 配置） |

> 注：默认设备端点为 `http://127.0.0.1:8089`（Zygisk hook 监听地址，适用于 yyb-go 与微信同机的 Magisk 场景）；纯 Docker 下该地址在容器内不可达，会自动回退原生扫码登录，不影响使用。

## 二、目录结构

```
wxcode-yyb-fusion-docker/
├── deploy.sh               # 一键部署脚本（生成 .env → 建数据目录 → 拉镜像 → 健康检查）
├── compose.yaml            # 默认编排：直接拉取预构建镜像（推荐）
├── compose.build.yaml      # 本地构建编排：改了源码后用这个
├── .env.example            # 环境变量模板（WXCODE_URLS 默认留空 = 原生模式）
├── .dockerignore
├── .github/workflows/
│   └── docker-publish.yml  # GitHub Actions：构建镜像并推送到 Docker Hub
├── Dockerfile              # 网关镜像（预编译二进制，amd64/arm64，内置 wxcode APK）
├── Dockerfile.src          # 网关镜像（源码构建版，可选）
├── build-gateway.sh        # 交叉编译 gateway/yyb-go-{amd64,arm64}（改源码后重建镜像用）
├── NOTICE.md               # 第三方来源与许可说明
├── README.md               # 仓库首页：一键部署入口
├── README-DOCKER.md        # 本文档（完整文档）
├── src/                    # 融合版完整源码（与预编译产物同源）
│   ├── cmd/yyb-go/         # 主程序入口
│   ├── internal/
│   │   ├── httpapi/        # 路由层：/login /scan /wxcode/* 统一分发（fusion.go）
│   │   ├── wxcode/         # wxcode 设备协议客户端（/login?appId= 取码）
│   │   ├── protocol/       # 原生微信登录协议（login_buffer / pool / mmtls）
│   │   ├── qr/             # 扫码登录二维码生成与轮询
│   │   └── store/          # 账号与登录态存储（SQLite）
│   └── ...
├── gateway/
│   ├── yyb-go-amd64        # Linux/amd64 预编译融合网关
│   ├── yyb-go-arm64        # Linux/arm64 预编译融合网关
│   └── resource/           # Web 面板静态资源（含 /scan 扫码页）
└── wxcode/
    └── wxcode_2.1.0.apk    # 可选：安装到已 root 手机作设备取码端点
```

## 三、快速开始（手动部署）

> 不想手敲就用根目录的 `./deploy.sh`（一键部署，见开头）；下面两种方式适合想自己控制的场景。

### 方式 A：拉取预构建镜像（推荐）

```bash
git clone https://github.com/SJZYKJ/wxcode-yyb-fusion-docker.git
cd wxcode-yyb-fusion-docker
cp .env.example .env        # 默认即可：WXCODE_URLS 留空 = 原生扫码登录
docker compose up -d        # 拉取 chungg/wxcode-yyb-fusion:latest 并启动
```

想固定版本 / 回滚，改 `.env` 里的 `IMAGE_TAG` 后重新 `up -d`。版本标签只有两种：
`v<N>` 是正式发布号（`v1`、`v2`、`v3`…，**全局递增、永不重置**，回滚就用它），
`latest` 永远指向最新一次构建（会动，别拿它做回滚）。

**当前跑的是哪个版本，登录页会直接告诉你**：页面左栏底部和右栏「版本更新」标题旁都会显示版本号，取的是镜像内的 `/static/version.json`（构建时由 `VERSION` 构建参数写入）。想从命令行核对：

```bash
docker compose exec yyb-go cat /app/resource/static/version.json   # -> {"version":"v7"}
# 或
curl -s http://127.0.0.1:8088/static/version.json
```

该文件是公开静态资源（不含敏感信息），所以不需要令牌。本地 `compose.build.yaml` 自建的镜像
不属于 `v<N>` 发布序列，这里会显示 `local`。

### 方式 B：本机自行构建

```bash
cp .env.example .env
docker compose -f compose.build.yaml up -d --build
```

> 两个 compose 文件的项目名、服务名、数据卷完全一致，可以随时互换，不会产生两套容器。

启动后：

| 地址 | 说明 |
|---|---|
| http://<host>:8088/ | Web 面板（注册首个账号为管理员） |
| http://<host>:8088/scan | 微信扫码登录页（手机微信扫码，自动存 login_buffer） |
| http://<host>:8088/login?appId=wx... | 取码接口（wxcode 协议；v4.2.1 起自动适配：设备优先，无 wxcode 设备时自动走原生扫码登录；多账号自动选默认账号） |
| http://<host>:8088/health | 健康检查 |
| http://<host>:8088/whoami | wxcode 兼容：微信进程/版本信息（v4.2.1）|
| http://<host>:8088/instances | wxcode 兼容：账号实例列表（v4.2.1）|

### 融合取码接口

```bash
# 1) 原生扫码登录（推荐）：/scan 扫码后，直接取码
curl -X POST http://127.0.0.1:8088/login -H 'Content-Type: application/json' \
  -d '{"app_id":"wxaa3a999db5d744c6"}'
# 返回: {"source":"native","fallback":false,"openid":"...","result":{"code":"...","status":"success"}}

# 2) wxcode 设备通道（需配置 WXCODE_URLS 指向已 root 手机）
curl 'http://127.0.0.1:8088/login?appId=wxaa3a999db5d744c6'

# 3) 设备状态
curl http://127.0.0.1:8088/api/wxcode/status

# 4) 设备侧 Zygisk hook 引导配置
curl 'http://127.0.0.1:8088/wxcode/hookcfg?v=8.0.76'

# 5) wxcode 接口自动适配（v4.2.1）：GET /login?appId= 设备优先，无 wxcode 设备时自动回退原生扫码登录；
#    响应仍为 wxcode 格式（err/msg/appId/status/code/codeType/codeLength），客户端无需改动
# 6) wxcode 兼容端点（无需 root 手机）
curl http://127.0.0.1:8088/whoami
curl http://127.0.0.1:8088/instances
```

## 四、多用户与账号隔离（v4.2.2）

同一网关可以注册多个用户，每个用户的账号互相独立，同时脚本取码接口保持"读全部"。

### 两类视角

| 视角 | 判定方式 | 能看到哪些账号 |
|---|---|---|
| 管理员 | 注册的第一个账号，或角色为 `admin`；未启用鉴权（`YYB_AUTH_DRIVER=none`）等价于本机管理员 | 全部账号 |
| 普通用户 | 由管理员创建 / 自行注册 | **只有自己扫码添加的账号**（`wechat_accounts.owner_user_id = 自己`） |
| 脚本 / 公开 API | 不带浏览器会话调 `/login`、`/instances`、`/wxapp/*`、`/wx/*` | 全部账号，但会跳过拥有者关闭了「脚本可读」的账号 |

### 普通用户能做什么

普通用户现在**不再被踢到个人设置页**，可以正常使用：

- `/` 工作台、`/scan` 扫码添加账号、`/runs` 运行管理、`/qr`、`/quick-login`
- `/accounts` 及其头像/刷新/重同步/备注/共享开关
- `/api/qinglong/*` 账号级任务操作
- `/settings` 个人设置（改密码、查看/踢出会话）

仅管理员可用：`/users` 用户管理、`/api/auth/users*`、注册开关、`/api/qinglong/config` 面板服务器级配置。
普通用户访问这些页面会被重定向到工作台 `/`，接口返回 403。

### 扫码归属

- 普通用户扫码 → 账号归到自己名下，别人（含管理员）在其列表里看不到；
- 管理员扫码 → 记在自己名下，但**不会夺走**已归属其他用户的账号；
- 老库升级：`owner_user_id` 为 `NULL` 的历史账号视为管理员名下，普通用户看不到。

### 「脚本可读」开关（api_shared）

账号拥有者可以在工作台选中账号后，用「脚本可读：开/关」按钮控制该账号是否对脚本暴露：

- **开（默认）**：顺丰/移动云盘等脚本通过 `/instances`、`/login`、`/wxapp/getCode` 能读到它；
- **关**：该账号从上述接口消失（`/instances` 不返回、按 `ref` 取码返回 `account not shared to api`），只保留在拥有者自己的控制台里。

> 说明：公开 API 不做归属过滤，是为了让脚本一次性跑遍所有账号（含管理员名下）。如果只想自己用、不想被脚本读到，把对应账号的「脚本可读」关掉即可。

### 相关接口

```bash
# 当前登录用户信息（含 role，前端据此决定是否显示用户管理入口）
curl -b cookie.txt http://127.0.0.1:8088/api/auth/me

# 开关某账号是否允许脚本读取（仅账号拥有者 / 管理员可调用）
curl -b cookie.txt -X PUT http://127.0.0.1:8088/accounts/share \
  -H 'Content-Type: application/json' -d '{"ref":"wx_xxx","shared":false}'
```

## 五、公开接口访问令牌（v4.2.3 引入，v4.2.4 改为强制）

取码类接口（`/login` 取码分支、`/instances`、`/whoami`、`/wxapp/*`、`/wx/*`、
`/wxcode/*`、`/openapi.json`）**默认就必须带访问令牌**（fail-closed）。

`YYB_API_TOKEN` 留空时不会裸奔，而是走这套自动流程：

1. 启动时读取数据库里已有的令牌（`app_settings.api_token`，跟随数据卷持久化）；
2. 没有就生成一个 256bit 随机令牌、写回数据库，并打印到容器日志：

```bash
docker compose logs | grep -A6 "已自动生成 API 访问令牌"
```

因此**容器重启、镜像升级都不会换令牌**，青龙里的 `YYB_API_TOKEN` 配一次即可。

想指定令牌就写进 `.env`（显式配置优先级最高，会覆盖数据库里的值）：

```bash
# .env
YYB_API_TOKEN=<一个足够随机的长字符串>     # 生成：openssl rand -hex 24
```

只有显式设置 `YYB_ALLOW_NO_AUTH=true` 才会真正关闭鉴权（启动日志会有醒目警告），
这是给「完全可信内网 / 前面已有强鉴权反代」留的逃生舱。

### 脚本侧怎么带令牌

三种方式任选，脚本改一处即可：

```bash
# 1. 请求头（推荐）
curl -H "Authorization: Bearer $YYB_API_TOKEN" http://host:8088/instances

# 2. 自定义头
curl -H "X-API-Token: $YYB_API_TOKEN" 'http://host:8088/login?appId=wxaa3a999db5d744c6'

# 3. 查询参数（给不方便加头的旧脚本兜底）
curl "http://host:8088/instances?token=$YYB_API_TOKEN"
```

> 前两种更安全：`?token=` 会出现在 Web 服务器访问日志、浏览器历史与 `Referer` 里。

Python（青龙脚本）里通常是加一行默认头：

```python
GATEWAY_TOKEN = os.environ.get("YYB_API_TOKEN", "").strip()
GATEWAY_HEADERS = {"Authorization": f"Bearer {GATEWAY_TOKEN}"} if GATEWAY_TOKEN else {}
# 然后把 GATEWAY_HEADERS 合并进 requests 的 headers 参数
```

> 顺丰中秋 / sfsy日常版 / 移动云盘 三个青龙脚本**已内置该适配**：只要在青龙环境变量里
> 配上 `YYB_API_TOKEN`（与网关里是同一个值），脚本会自动在
> `/instances`、`/wxapp/getCode`、`/login` 请求上带 `Authorization: Bearer`；
> 令牌缺失或写错时脚本直接打印 401 提示并停止，不会静默失败或空转重试。

### 哪些接口**不会**被令牌拦住

| 接口 | 说明 |
|---|---|
| `/health` | 容器健康检查，必须永远开放 |
| `/static/*` | 登录页等静态资源 |
| `POST /login`（带 `username`） | Web 控制台登录，否则配了令牌就没人能登录 |
| `GET /login`（不带 `appId`） | 登录页面本身 |
| `/register`、`/logout` | 注册页与登出（注册另有开关，见下） |
| 任何带**有效控制台会话 Cookie** 的请求 | 工作台「调用配置」就是在浏览器里带会话调 `/wxapp/*`、`/wx/code` 的。**会话不等于令牌**：会话请求按控制台权限过滤，普通账号只能读到归属自己的账号（见下方「会话 ≠ 令牌」） |

> `/wxcode/*`（设备 hook 引导配置与心跳注册）在 v4.2.4 起**也纳入令牌保护**：
> 注册接口写入的端口会被 `deviceEndpoints()` 当作 `http://127.0.0.1:<port>` 去请求，
> 不鉴权等于对外开放了一个取码源劫持/SSRF 点。手机端 hook 若需调用，请在 URL 上带
> `?token=`；**推荐直接用 `WXCODE_URLS` 静态配置设备端点**（Docker 部署的正常做法）。

> 判定逻辑与 `/login` 自身的分发一致（有 `username` 且无 `app_id` 才算控制台登录），
> 因此无法靠同时带上 `app_id` 绕过取码鉴权。令牌比较使用常量时间函数。

### 其它安全默认值（v4.2.4）

| 项目 | 默认 | 说明 |
|---|---|---|
| 公开注册 | **关闭** | 库中无账号时，仅允许**内网/回环地址**完成首个管理员注册；公网来源返回 403。要公开注册设 `YYB_ALLOW_REGISTRATION=true`，或用 `YYB_ADMIN_USER`/`YYB_ADMIN_PASSWORD` 预先指定管理员。 |
| 登录限速 | 账号 10 次 / IP 8 次，15 分钟窗口 | 双通道计数，只伪造 IP 或只打单个账号都绕不过去；计数表有上限，不会被刷爆内存。 |
| `X-Forwarded-For` | **不信任** | 只有 `YYB_TRUST_PROXY=true` 才采信，供反代场景使用。直连暴露时保持默认，否则伪造该头即可绕过限速。 |
| 会话 Cookie | `HttpOnly` + `SameSite=Lax` | 请求为 HTTPS（直连 TLS 或 `X-Forwarded-Proto: https`）时自动加 `Secure`；也可 `YYB_COOKIE_SECURE=true` 强制。 |
| 登录 `next` 参数 | 只接受站内路径 | `//host`、`/\host` 与控制字符注入一律收敛到 `/`，无开放重定向。 |
| 容器权限 | 非 root（`yyb`） | `cap_drop: ALL` + `no-new-privileges`，仅保留启动时修数据目录权限所需的 5 个 capability。 |

### 会话 ≠ 令牌：两条通道的可见范围不同（v4.2.5）

同一个取码接口，**按你用什么凭据**决定能看到/取到哪些账号：

| 凭据 | 权限模型 | 可见 / 可取的范围 |
|---|---|---|
| API 令牌（`Authorization` / `X-API-Token` / `?token=`） | 公开 API（脚本通道） | 全部 `api_shared=1` 的账号，**不做归属过滤** —— 青龙脚本走这条，行为与以前完全一致 |
| 浏览器会话 Cookie | 控制台（按归属） | 管理员：全部账号；普通账号：**只有归属自己的账号** |

> ⚠️ **v4.2.4 及更早版本存在横向越权**：任何登录用户（哪怕是最普通的账号）用浏览器
> 打开 `/instances` 就能列出**全部**账号的 openid，还能借 `api_shared` 默认开启按 `ref`
> 取到他人（含管理员）的 code。原因是「有效会话」被当成与令牌等价的凭据放行，却没有把
> 会话身份注入请求上下文，下游 handler 于是按「无会话的公开 API 调用」处理。v4.2.5 已按
> 上表修正；越权请求返回 `account not found`，不泄露账号是否存在或归属。
>
> 另外 `/wxcode/hookcfg`、`/wxcode/config`、`/wxcode/register` 现在只接受
> **令牌或管理员会话**（未认证 401、普通会话 403）—— 它们写的是全局取码源端口表，
> 普通用户不应能篡改。

### ⚠️ 一个组合要注意

浏览器会话是被令牌放行的条件之一。如果你把 Web 鉴权整个关掉
（`YYB_AUTH_DRIVER=none`），浏览器就没有会话可用，此时工作台的「调用配置」
也会被令牌拦住。**建议保持默认的 `YYB_AUTH_DRIVER=sqlite`**；
确实要关鉴权时，请直接用 `curl` 带令牌调用取码接口。

## 六、默认端点说明（127.0.0.1:8089）

网关内置的默认设备端点是 `http://127.0.0.1:8089`（对应 Zygisk hook 引导配置 `/wxcode/hookcfg` 输出的端口，供"网关与微信同机运行"的 Magisk 场景使用）。
纯 Docker 部署时容器内没有该端点，`/login` 请求会自动**回退到原生扫码登录通道**，无需任何处理；若不想看到状态页里的"离线端点"，可在 .env 显式设置 `WXCODE_URLS=` 之外的其他值或直接忽略。

## 七、wxcode 设备接入（可选）

如果你希望保留 wxcode 模块的取码能力（多设备轮换、分身微信等），步骤：

1. 准备一台已 root 的 Android 手机，安装 LSPosed 框架；
2. 安装 `wxcode/wxcode_2.1.0.apk` 并启用模块，作用域勾选微信；
3. 重启微信，确认手机端 `http://127.0.0.1:8088/instances` 有实例；
4. 在 .env 中配置 `WXCODE_URLS=http://<手机IP>:8088`（同一局域网；或用 adb reverse）；
5. `docker compose up -d` 重启网关即可。

> 注意：wxcode 手机端 NanoHTTPD 默认只监听 8088（主实例），若手机端还跑了 Zygisk hook（8089）可同时填多个端点：`http://ip:8088,http://ip:8089`。

融合网关的 `/login` POST 接口会自动选择通道：
- 只传 `app_id` → 先设备（wxcode）后原生；
- 传 `app_id + ref` → 先原生（指定账号）后设备；
- 传 `prefer=device|native` → 显式指定。

## 八、与"旧版三容器方案"的区别

v3.0.0 及更早的 docker-deploy 曾用 redroid（Android-in-Docker）把微信 APK 装进容器，
需要宿主机内核 binder/ashmem 支持，普通云 VPS / macOS / WSL2 大多不可用，且要维护
微信登录态，非常脆弱。

v4.0.0 起改为**单服务纯 Docker**：
- 扫码登录走 YYB Go 原生协议，完全不需要 Android 环境；
- wxcode 仅作为可选外部设备端点（手机本来就能装），不引入容器依赖；
- 镜像内仍内置 wxcode_2.1.0.apk（`/app/wxcode/`），供需要设备通道时取出安装。

- v4.2.4：**安全加固（默认即安全）**——
  ①取码接口改为 fail-closed：`YYB_API_TOKEN` 未配置时自动生成 256bit 令牌并写入 `app_settings`（跟随数据卷持久化，重启不变），启动日志醒目打印，不再存在「无鉴权」窗口；只有显式 `YYB_ALLOW_NO_AUTH=true` 才关闭鉴权。
  ②`/wxcode/hookcfg`、`/wxcode/config`、`/wxcode/register` 纳入令牌保护（注册接口写入的端口会被 `deviceEndpoints()` 当取码源请求，不鉴权等于开放 SSRF/取码源劫持点）。
  ③公开注册默认关闭；库中无账号时仅允许内网/回环地址完成首个管理员注册，公网来源 403，堵住「公网抢注管理员」；新增 `YYB_ALLOW_REGISTRATION=true` 显式开放。
  ④登录失败限速改为「账号 + 来源 IP」双通道（10 次 / 8 次，15 分钟窗口）并给计数表加上限；默认不再信任 `X-Forwarded-For`，新增 `YYB_TRUST_PROXY=true` 供反代场景显式开启。
  ⑤修复登录 `next` 参数的开放重定向（`/\host` 这类反斜杠变体此前可跳出站外）。
  ⑥会话 Cookie 在 HTTPS（含 `X-Forwarded-Proto: https`）下自动带 `Secure`。
- v4.2.3：**公开接口可选访问令牌**——新增 `YYB_API_TOKEN`，填了就要求取码接口（`/login` 取码分支、`/instances`、`/whoami`、`/wxapp/*`、`/wx/*`、`/openapi.json`）带 `Authorization: Bearer` / `X-API-Token` / `?token=` 令牌或有效的控制台会话，留空则行为与旧版完全一致；`/health`、网页登录（`POST /login` 带 `username`）、`/wxcode/*` 始终放行（`/wxcode/*` 在 v4.2.4 起改为需要令牌）。同时把部署编排拆成 `compose.yaml`（拉镜像）与 `compose.build.yaml`（本地构建），数据目录改为可用 `DATA_DIR` 覆盖的相对路径，并新增 GitHub Actions 发布流水线（amd64+arm64）。
- v4.2.2：**多用户账号隔离**——普通用户不再被重定向到个人设置页，可正常使用工作台/扫码添加/运行管理；账号按 `owner_user_id` 归属，普通用户只能看到并管理自己扫码添加的账号，管理员看全部；新增账号级「脚本可读」开关（`api_shared`），公开取码接口（`/login`、`/instances`、`/wxapp/*`）默认仍可读全部账号，拥有者可单独关闭某个账号的对外读取。同时修复 `internal/httpapi` 测试因 `t.Context()` 需要 go1.24 而无法编译的问题。
- v4.2.1：修复部署时 SQLite `unable to open database file: out of memory (14)`——bind-mount 数据目录（./data/*）属主为 root，非 root 的 yyb 用户无法写入；新增 entrypoint 以 root 启动并自动修复数据目录权限后降权运行，compose 保留最小能力集（CHOWN/FOWNER/DAC_OVERRIDE/SETUID/SETGID）。
- v4.2.0：修复多原生账号时 `multiple native accounts configured` 报错——`GET /login?appId=` 无设备回退原生取码时自动选用「存活（alive）优先、其次最近更新」的账号，响应带 `openid` 标明实际账号；支持 `&ref=<openid|id>` 显式指定账号。
- v4.1.0：GET /login?appId= 兼容 wxcode 接口（设备优先，无设备自动回退原生扫码登录）；新增 /whoami、/instances 兼容端点。

## 九、常见问题

- **WXCODE_URLS 留空时 /login?appId= 能用吗？** 能（v4.1.0 起）：设备通道不可达时自动回退原生扫码登录，响应保持 wxcode 格式（err/msg/appId/status/code/codeType/codeLength），客户端无需改动；没有 root 手机也能用。多账号时自动选存活/最近更新账号，响应 `openid` 标明实际账号；需指定账号可传 `&ref=<openid|id>`。
- **/api/wxcode/status 显示 127.0.0.1:8089 offline？** 正常，同上；显式配置 WXCODE_URLS 后可看到手机端在线状态。
- **没有 root 手机能不能用？** 能。原生扫码登录完全够用，wxcode 设备通道只是锦上添花。
- **面板登录不上？** 首个管理员从**内网**访问 `/register` 注册即可（自动成为管理员，v4.2.4 起公网来源会被拒绝）；也可以在 `.env` 里设 `YYB_ADMIN_USER` / `YYB_ADMIN_PASSWORD` 后重启，网关会自动创建该管理员。完全不需要 Web 鉴权时可设 `YYB_AUTH_DRIVER=none`（注意：此时工作台的「调用配置」会被访问令牌拦住，见第五节）。
- **普通用户点「添加账号/运行管理」被跳到个人设置？** v4.2.2 已修复：这些页面不再要求管理员权限，普通用户可正常使用，只是账号列表里只有自己的账号。
- **「账号运行管理」页显示 0 个脚本？** v8 起这个列表**直接读青龙脚本目录里的 `.js`/`.py` 文件**（走 OpenAPI `/open/scripts/files`），不再依赖定时任务，也不再有任何目录白名单配置 —— 以前为了「有哪些脚本」去读「有哪些定时任务」，等于让用户再手工维护一遍同样的东西，还会把任务和脚本两件事搅在一起。看页面上的空列表提示就能定位：
  1. 「青龙的脚本目录里一个文件都没有」→ 先去青龙的「脚本管理」上传脚本；
  2. 「有文件，但没有能挂给账号的脚本」→ 那些文件被判定成了工具/依赖（`SendNotify.py`、`wechat_tools.js`/`.py`、`notify.js`、`!RunAll.py`，以及 `node_modules`、`.git` 等目录下的文件会被自动排除）；
  3. 「面板没有提供脚本文件列表」→ 你的面板（老青龙 / 代代）没有这个接口，网关已自动退回从定时任务推断，此时列表只包含命令能解析出脚本的任务。
  列表上方可按目录筛选；在青龙里已有全局任务的脚本会显示「重复执行」提示，因为那时它和账号任务会各跑一遍。
  左下角「青龙已连接」只说明 OpenAPI 凭据能换到 token，和脚本列表是两条独立路径，**已连接 + 0 脚本是正常组合**。
- **点「运行账号」会跑哪些账号？** 只会跑你选中的那一个（v9 起）。网关创建的账号任务在任务前命令里把 `WECHAT_OPENIDS` 覆写成该账号的 openid，脚本因此不会再回网关枚举全部账号；**旧的托管任务在你下次点「运行」或拨动定时开关时会自动带上这个限定**。
- **打开脚本的「定时」开关又会跑哪些账号？** 按**网关登录账号**隔离：跑该登录账号名下的**全部 code 账号**（v10 起）。一个登录账号 + 一个脚本只建一条定时任务，任务前命令里的 `WECHAT_OPENIDS` 是该登录账号名下所有 openid 的列表，所以不会重复执行、也不会跑到别人的账号上。**手动「运行」与「定时」是两条不同的任务**，因为青龙的任务前命令是建任务时写死的文本，一条任务只能有一种账号范围。
- **「脚本目录可见范围」是干什么的？** 管理员在工作台的「脚本目录可见范围」里填一组目录（如 `code脚本`），非管理员账号就**只能看到这些目录下的脚本**：列表里不出现，直接调接口运行或开定时也会被拒（不是只藏起来）。留空 = 不限制。管理员自己不受限。
- **升级到 v10 后，我以前给每个 code 账号单独开的定时怎么办？** 不用手改：打开一次「账号运行管理」就会自动把同一个脚本的旧任务收敛成一条登录账号级任务，并沿用「原来至少有一个账号在启用」的状态。收敛只**停用**旧任务、不删除，所以旧任务仍可作为「只跑该账号」的手动运行入口。
  注意这只约束网关建的任务：**在青龙里直接运行同一个脚本不受影响**，仍按青龙的全局 `WECHAT_OPENIDS` 取账号（写 `ALL` 即全部账号）—— 想一次跑全部账号，用青龙里自己的任务就行。谁能操作哪个账号仍按归属校验，普通用户在网关上碰不到别人的账号。
- **普通用户扫码添加的账号，管理员能看到吗？** 不能。为保护账号隐私，账号只对其归属用户可见；管理员能看到的是"未归属"（老数据）和自己名下的账号。如需统一管理，可由该用户自行在控制台操作，或将其角色提升为 `admin`。
- **脚本还能读到所有账号吗？** 能（v4.2.2 起默认不变）。公开接口不做归属过滤，只是会跳过拥有者关闭了「脚本可读」的账号。反之，某个用户不想让自己的账号被脚本读到，在工作台选中该账号点「脚本可读：关」即可。
- **公网部署安全吗？** v4.2.4 起**默认安全**：取码接口强制鉴权（令牌未配置会自动生成并落库，不是裸奔），公开注册默认关闭且首个管理员只能从内网注册，登录限速不可被伪造 `X-Forwarded-For` 绕过。仅当你显式设置 `YYB_ALLOW_NO_AUTH=true` 或 `YYB_ALLOW_REGISTRATION=true` 时才需要额外评估；另外建议公网暴露时把 `--bind` 设为 `127.0.0.1` 交给反代，并设 `YYB_TRUST_PROXY=true`、开启 HTTPS。
- **填了 YYB_API_TOKEN 之后脚本报 401？** 给脚本的网关请求加上 `Authorization: Bearer <令牌>` 或 `X-API-Token: <令牌>` 请求头，也可以直接在 URL 上拼 `?token=<令牌>`，见第五节。
- **没配 YYB_API_TOKEN，脚本却报 401？** v4.2.4 起网关会自动生成令牌，去容器日志里取：`docker compose logs | grep -A6 已自动生成`，把它填到青龙的 `YYB_API_TOKEN`；或在 `.env` 里显式指定一次再重启。
- **升级后容器重启，令牌会变吗？** 不会。自动生成的令牌存在数据库（`app_settings.api_token`），跟随数据卷持久化。**除非你把数据卷删了**（那等于换了个新实例，需要在青龙里同步更新）。
- **升级后注册页打不开了？** v4.2.4 起公开注册默认关闭。库中还没有账号时，从**内网**访问 `/register` 仍可注册首个管理员；已有账号后需要管理员在「用户管理」里创建，或临时设 `YYB_ALLOW_REGISTRATION=true`。
- **wxcode 手机 hook 的 `/wxcode/register` 报 401？** v4.2.4 起该接口纳入令牌保护。推荐改用静态配置：`.env` 里 `WXCODE_URLS=http://<手机IP>:8088`（Docker 部署的标准做法），就不需要 hook 自注册。
- **填了令牌后浏览器登不上控制台？** 不会。`POST /login`（带 `username`）与登录页始终放行，工作台用会话 Cookie 访问接口也不受令牌限制。
- **改了源码怎么重新出镜像？** ① `./build-gateway.sh`（交叉编译 amd64+arm64）→ ② `cp -r src/resource/templates/. gateway/resource/templates/` → ③ `docker compose -f compose.build.yaml up -d --build`。只改前端模板的话第①步可以跳过。
- **怎么发新版本到 Docker Hub？** 推送到 `main` 分支即可，GitHub Actions 会自动构建双架构镜像并打上 `latest` 与 `v<N>` 两个标签（提交 SHA 记录在 Release 说明里）。注意：改 Go 代码必须先跑 `build-gateway.sh` 并提交 `gateway/` 里的二进制，CI 只做打包不做编译。
  版本号按两条路决定：**本次提交上已经有人工打的** `v<N>` / `v<N>.<M>` 标签就直接沿用（想要指定的号，例如发 `v7.1`：`git tag v7.1 && git push origin v7.1` **先推标签**，再 `git push origin HEAD:main`）；没有就取全仓库最大主号 + 1（`v7.1` 之后自动是 `v8`、`v9`…）。
- **怎么删掉发错的镜像标签？** 本地通常连不上 `hub.docker.com`（`auth.docker.io` 也常不通），所以走 CI：Actions → **「删除 Docker Hub 镜像标签（维护）」** → Run workflow，`tags` 填 `v7 v8 v9`，`confirm` 填 `DELETE`。**它只删 Docker Hub 上的镜像，Git 标签和 Release 要另外删**（`git push origin --delete v7`、GitHub Releases 页面删对应条目）——序号取自「现有 Git 标签的最大值 + 1」，留着一个作废标签会让后续号跳号，所以两边要一起清。
- **想把某个已有镜像改个标签名（不重新构建）？** 跑 `Retag (重新挂标签，不重建)` 工作流，填源标签和目标标签即可；它用 `docker buildx imagetools create` 直接改 registry 里的 manifest，几秒钟完成，层数据不动。
- **数据在哪？** 默认 `${DATA_DIR:-./data}/db`（SQLite）、`/qr`（二维码）、`/avatars`，重启不丢；换盘位置改 `.env` 里的 `DATA_DIR`。
