# 更新日志

本项目（wxcode-YYB）按实际提交时间记录主要功能变化，便于部署后确认版本内容。

## 2026-11-12

### v17 · 添加多个取码渠道 + 重构界面

添加腾讯电脑管家、腾讯手游助手、雷电模拟器、QQ浏览器等取码渠道，同一微信号多渠道取码可合并；重构界面。

下面是与 **v16**（上一个线上发布版）逐项核对后的差异：功能、接口、变量、设置、数据库。

#### 一、取码渠道：1 条 → 5 条

| 渠道 | ID | 说明 |
| --- | --- | --- |
| 应用宝（YYB） | `appstore` | 原有渠道，仍是默认 |
| 腾讯电脑管家 | `pcmgr` | 新增 |
| 腾讯手游助手 | `gameassist` | 新增 |
| 雷电模拟器 | `ldplayer` | 新增（实验） |
| QQ浏览器 | `qbbrowser` | 新增 |

- v16 的扫码链路是单文件 `internal/qr/qr.go`（335 行），没有渠道概念；v17 把它拆成 `qr.go` + `channels.go`，渠道清单由 `qr.ScanChannels()` 统一下发，扫码页、多渠道路径、账号卡片、设置页「扫码渠道」都从同一份清单渲染，新增渠道不用改页面。
- 账号记录新增 `scan_product`（渠道来源），控制台与接口都能按渠道区分账号。
- **同一微信号多渠道取码可合并**：`POST /accounts/merge` 把同一 openid 的多条凭据合成一条，保留可用凭据；账号矩阵用 `fleet_primary_id` 标记主账号，配套 `/api/fleet`、`/api/fleet/bind-many`、`/api/fleet/dissolve`。
- 新增渠道设置与能力探测接口：`GET /api/scan-channels`、`/api/scan-settings`。

#### 二、控制台界面

- 新外壳：顶部横幅 + 分组侧栏 + 底部工具栏（移动端抽屉），一级入口为「工作台 / 我的微信账号 / 添加账号 / 运行与日志 / 代理设置」，管理后台分组为「用户管理 / 运行日志」；支持明暗主题与 20 个自绘图标，断点 1279 / 899 / 560。
- 新增页面：代理设置（`/proxies`）、已激活短链接（`/account-links`）、系统日志（`/logs`）、多账号一次性短链接页（`account-link.html`）；运行管理页拆成「运行与日志」并新增 `logs.html`。
- 账号列表改为卡片网格 + 账号矩阵视图，新增账号级操作：压缩 ID（`/accounts/compact`）、修复（`/accounts/repair`）、状态裁决（`/accounts/status`，人工确认过期后会摘掉青龙绑定与缓存会话）、账号代理（`/accounts/proxy`、`/accounts/proxy/test`）。
- 整体视觉收敛（`skin-clean.css`），普通用户界面不出现管理入口。
- 登录页「版本更新」改为：当前版本（v17）保留一段说明，历史版本（v16 → v2）只列标题，列表超过一屏时在卡片内滚动并带底部渐隐提示。

#### 三、账号、代理与短链接

- 代理：账号级代理 + 代理档案 `/api/proxy-profiles`（含 `/*path`）、按地区推荐节点 `/api/proxy-location/recommend`，配套 `internal/proxysource`。
- 一次性短链接：`/account-links`、`/account-link/:token`（含 `/*path`）、`/api/account-links`，令牌用 `YYB_ACCOUNT_LINK_KEY` 加密落库，支持单账号与多账号两种链接。
- 取码失败诊断：新增 `credential_diagnostics.go`，把电脑管家 / 手游助手 / 雷电 / QQ 浏览器等渠道的类型化拒绝原因归成可公开的结论，不透出凭据、OpenID 与代理认证。

#### 四、取码防护与节流（全新）

- 熔断与限速（`circuit.go`、`pacing.go`）：`YYB_CIRCUIT_BREAKER`（默认 `on`）、`YYB_CODE_MIN_GAP`（`3s`）、`YYB_CODE_MAX_WAIT`（`20s`）、`YYB_LOGIN_BUDGET`（`3`）、`YYB_LOGIN_WINDOW`（`10m`）。
- 这些参数可以在线调，不必重启：`/api/runtime-settings`、`/api/circuit-settings`、`/accounts/circuit-settings`；设置页新增「协议熔断总控」「运行参数」两个区块。环境变量只作为默认值。
- AppID 黑白名单：`YYB_CODE_DENY_APPIDS` / `YYB_CODE_ALLOW_APPIDS`，接口 `/api/appid-deny`；另有学习型动态拒绝 `YYB_CODE_DYNAMIC_DENY`、`YYB_CODE_DYNAMIC_DENY_THRESHOLD`。

#### 五、协议与会话

- 新增 H5 取码链路：`POST /wxapp/getH5Code`、`/wx/h5code`、`/wx/getlatestuserkey`（`internal/protocol/h5code.go`）。
- 新增 autoauth、设备标识、登录预算、失败码恢复：`YYB_DEVICE_IDENTITY`（`stable` 默认 / `random`）、`YYB_ENABLE_PC_LOGIN`（默认 `false`）、`YYB_FRESH_SESSION_WINDOW`、`YYB_APPSTORE_REFRESH_MODE`（`legacy_auth` 默认 / `source`）。
- `internal/protocol` 由 10 个文件增至 20 个（新增 `autoauth.go`、`device.go`、`h5code.go`、`login_budget.go`、`code_recovery.go`、`local_lab.go`）。
- 协议令牌 `YYB_PROTOCOL_TOKEN`：与 `YYB_API_TOKEN` 是两道独立闸门，留空即不启用。

#### 六、面板、集成与通知

- 青龙：脚本目录统一为 `qinglong/scripts`，新增 `YYB_QINGLONG_REF_MODE`（`id` 默认 / `openid`）、`YYB_QINGLONG_REPO` 显式配置、脚本管理接口 `/api/qinglong/sync-all`（`panel_scripts.go`）。
- 新增 Arcadia 面板（`panel_arcadia.go`）。
- 集成接口：`YYB_INTEGRATION_TOKEN` + `/integration/accounts`、`/integration/accounts/proxy`、`/integration/actions/get-code`、`/integration/actions/refresh-account`、`/integration/module-manifest.json`（`integration.go`）。
- 掉线通知：`YYB_NOTIFY_CHANNEL`（`serverchan` / `pushplus` / `qywx`，留空关闭）、`YYB_NOTIFY_TOKEN`、`YYB_NOTIFY_TOPIC`、`YYB_NOTIFY_ACCOUNT_PUSH`（默认 `true`）。
- 系统日志：`/logs` + `/api/system-logs`，新增 `internal/eventlog` 落盘事件日志。

#### 七、版本与镜像

- 新增 `GET /api/version`（`version` / `commit` / `build_date` / `update_url`）与 `/static/version.json`，登录页版本号取自这里。
- 镜像发到 Docker Hub `chungg/wxcode-yyb-fusion`，构建时把发布号写入镜像（`VERSION` / `ARG VERSION` 两处一致，有测试守着）。

#### 八、配置变量：16 项 → 40 项（无删除）

v16 的 16 个 `YYB_*` 全部保留且默认值不变，新增 24 项：

| 分组 | 变量 | 默认值 |
| --- | --- | --- |
| 注册邮箱 | `YYB_SMTP_HOST` / `YYB_SMTP_PORT` / `YYB_SMTP_USERNAME` / `YYB_SMTP_PASSWORD` / `YYB_SMTP_FROM` | 留空 = 不启用 |
| 注册邮箱 | `YYB_SMTP_TLS` | `true` |
| 令牌 | `YYB_PROTOCOL_TOKEN` | 留空 = 不启用 |
| 令牌 | `YYB_INTEGRATION_TOKEN` | 留空 = 不启用 |
| 青龙 | `YYB_QINGLONG_REF_MODE` | `id` |
| 青龙 | `YYB_QINGLONG_REPO` | 留空 |
| 协议 | `YYB_ENABLE_PC_LOGIN` | `false` |
| 协议 | `YYB_DEVICE_IDENTITY` | `stable` |
| 协议 | `YYB_APPSTORE_REFRESH_MODE` | `legacy_auth` |
| 通知 | `YYB_NOTIFY_CHANNEL` / `YYB_NOTIFY_TOKEN` / `YYB_NOTIFY_TOPIC` | 留空 = 关闭 |
| 通知 | `YYB_NOTIFY_ACCOUNT_PUSH` | `true` |
| 防护 | `YYB_CIRCUIT_BREAKER` | `on` |
| 防护 | `YYB_CODE_MIN_GAP` / `YYB_CODE_MAX_WAIT` | `3s` / `20s` |
| 防护 | `YYB_LOGIN_BUDGET` / `YYB_LOGIN_WINDOW` | `3` / `10m` |
| 防护 | `YYB_CODE_DENY_APPIDS` / `YYB_CODE_ALLOW_APPIDS` | 留空 |

另有 8 项只在源码编排（`src/.env.example`）里出现，容器部署可以不设：`YYB_ACCOUNT_LINK_KEY`、`YYB_CODE_DYNAMIC_DENY`、`YYB_CODE_DYNAMIC_DENY_THRESHOLD`、`YYB_DOCKER_NETWORK`、`YYB_IMAGE`、`YYB_UPDATE_PROXY`、`YYB_UPDATE_VERSION_URL`、`YYB_FRESH_SESSION_WINDOW`。

#### 九、接口：57 → 95 条（新增 38 条，无删除）

v16 的 57 条路由全部保留，新增：

- 渠道与扫码：`/api/scan-channels`、`/api/scan-settings`
- 账号整理：`/accounts/compact`、`/accounts/repair`、`/accounts/status`、`/accounts/merge`
- 账号代理：`/accounts/proxy`、`/accounts/proxy/test`、`/api/proxy-profiles`、`/api/proxy-profiles/*path`、`/api/proxy-location/recommend`
- 账号矩阵：`/api/fleet`、`/api/fleet/bind-many`、`/api/fleet/dissolve`
- 短链接：`/account-links`、`/account-link/:token`、`/account-link/:token/*path`、`/api/account-links`、`/api/account-links/*path`
- 防护与运行时设置：`/accounts/circuit-settings`、`/api/circuit-settings`、`/api/runtime-settings`、`/api/appid-deny`
- 协议：`/wx/h5code`、`/wx/getlatestuserkey`、`/wxapp/getH5Code`、`/api/qbbrowser/token`
- 日志：`/logs`、`/api/system-logs`
- 集成：`/integration/accounts`、`/integration/accounts/proxy`、`/integration/actions/get-code`、`/integration/actions/refresh-account`、`/integration/module-manifest.json`
- 面板：`/api/qinglong/sync-all`
- 页面与元信息：`/proxies`、`/api/version`、`/healthz`

#### 十、数据结构与升级注意

- 新增 `scan_product`、`fleet_primary_id` 两个账号字段；账号 JSON 不再返回 `owner_user_id`（归属改为 auth 库的 `account_owners` 表，用 `AccountOwner` 反查；`runs` 里的任务对象仍带 `owner_user_id`）。
- 老库升级会自动补 `wechat_accounts.api_shared` 列（默认 1，脚本可读），不需要手工迁移。
- **升级注意**：v16 把账号归属存在 `wechat_accounts.owner_user_id`，v17 改为 auth 库 `account_owners`，且没有针对老列的自动迁移。用 v16 的数据目录直接升级时，老账号在新库里没有归属记录 —— 管理员仍能看到和管理，普通用户看不到。恢复办法：用原账号重新扫一次（`ClaimAccount` 会把已有账号绑回当前用户），或由管理员代管。
- 删除 `internal/httpapi/fusion.go.bak`、`fusion.go.bak_autoadapt`、`fusion.go.bak_v41` 三个历史备份文件。

#### 十一、规模变化（同一仓库口径）

| 指标 | v16 | v17 |
| --- | --- | --- |
| Go 文件 | 65 | 254 |
| 业务代码行 | 14,287 | 31,178 |
| 测试代码行 | 5,301 | 23,953 |
| internal 包 | 6 | 13 |
| httpapi 业务文件 | 46 | 125 |
| 页面模板 | 7 | 11 |
| 前端 JS / CSS | 1 / 2 | 7 / 7 |
| 青龙脚本 | 13 | 212 |
| 文档 | 0 | 59 |
| HTTP 路由 | 57 | 95 |
| 配置变量 | 16 | 40 |
