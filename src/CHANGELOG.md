# 更新日志

本项目按实际提交时间记录主要功能变化，便于部署后确认版本内容。

## 2026-09-17

> ⚠️ 仓库维护提示：GitHub 会扫描**整条提交信息**（含正文）里的跳过标记
> （`[skip ci]` / `[ci skip]` / `[no ci]` / `[skip actions]` / `[actions skip]`），
> 命中就整个 workflow 不触发。所以「在正文里解释为什么**没有**加 `[skip ci]`」
> 会导致这一行文字把构建也一起跳过——想触发构建时，正文里也别出现这些字面量。

- 安全加固：公开取码接口由「可选鉴权」改为 **fail-closed**。新增 `internal/httpapi/security.go` 的 `resolveAPIToken`：`YYB_API_TOKEN` 未配置时不再放行，而是优先复用数据库 `app_settings.api_token` 里已有的令牌（跟随数据卷持久化，容器重启/升级不变），没有就生成 256bit 随机令牌并写回 + 打印到启动日志。**只有显式设置 `YYB_ALLOW_NO_AUTH=true` 才会真正关闭鉴权**（启动日志有醒目警告）。
- 安全加固：`/wxcode/hookcfg`、`/wxcode/config`、`/wxcode/register` 从「始终开放」移入访问令牌保护组。原因是注册接口写入的端口会被 `deviceEndpoints()` 拼成 `http://127.0.0.1:<port>` 去请求，未鉴权时等于对外开放了一个 SSRF / 取码源劫持点。
- 安全加固：公开注册默认**关闭**（`RegistrationEnabled` 缺省值由 true 改为 false）。新增 `registrationAllowed`：库中还没有任何账号时，仅允许**内网/回环/CGNAT 地址**完成首个管理员注册，公网来源返回 403 并提示改用 `YYB_ADMIN_USER`/`YYB_ADMIN_PASSWORD`；新增 `YYB_ALLOW_REGISTRATION=true` 显式开放公开注册。堵住「实例刚暴露就被陌生人抢注管理员」（首个注册者自动成为 admin）。
- 安全加固：登录失败限速改为「账号 + 来源 IP」双通道（`loginFailPerUser`=10 / `loginFailPerIP`=8，15 分钟窗口），任一超限即拒绝；计数表加上限（4096）并在满时淘汰过期/最早条目，避免被刷爆内存。
- 安全加固：`clientIP` 默认**不再信任** `X-Forwarded-For` / `X-Real-IP`（原先无条件采信，攻击者每次换一个伪造 IP 即可完全绕过按 IP 的限速）；新增 `YYB_TRUST_PROXY=true` 供确实部署在反代后面的场景显式开启。
- 修复一个开放重定向：`safeNext` 原先只挡 `//host`，而按 WHATWG URL 规范浏览器会把路径里的反斜杠规范化为 `/`，因此 `/\evil.com`、`/\/evil.com` 可跳出站外；现一并拒绝反斜杠并剔除控制字符（防止响应头注入）。
- 会话 Cookie：新增 `cookieSecure`，请求为 HTTPS（直连 TLS 或 `X-Forwarded-Proto: https`）时自动加 `Secure`，仍可用 `YYB_COOKIE_SECURE=true` 强制。
- 新增回归测试 `internal/httpapi/security_test.go`（令牌自动生成与持久化、`/wxcode/*` 鉴权、注册引导窗口、`safeNext`、`clientIP`、伪造 XFF 下的登录锁定）与 `cmd/yyb-go/main_test.go`（`envBool` 字面量解析，防止环境变量接线漏掉后静默失效）。

- 修复普通用户（非管理员）登录后没有实际功能的问题：`/scan`、`/runs`、`/qr`、`/quick-login`、`/accounts` 及账号级 `/api/qinglong/*` 原先统一挂在管理员路由组下，导致点击「添加账号 / 运行管理」都被重定向到个人设置页、也无法扫码添加微信；现改为所有已登录用户可用，仅 `/users`、`/api/auth/users*`、注册开关和 `/api/qinglong/config` 仍限管理员。
- 新增账号归属（`wechat_accounts.owner_user_id`）：普通用户只能看到并管理自己扫码添加的账号，管理员可见全部；`NULL` 归属的历史账号按管理员名下处理。老库启动时自动补列并建索引，无需手工迁移。
- 新增账号级「脚本可读」开关（`wechat_accounts.api_shared`，默认开）：公开取码接口 `/login`、`/instances`、`/wxapp/*`、`/wx/*` 不做归属过滤，仍能读到所有账号（含管理员名下的），但会跳过拥有者关闭了该开关的账号，兼顾脚本批量取码与账号隐私。
- 新增 `PUT /accounts/share` 控制台接口与工作台「脚本可读：开/关」按钮，账号拥有者可单独关闭某个账号的对外读取。
- 普通用户访问管理员专属页面时改为重定向到工作台 `/`（原先重定向到 `/settings`），接口返回 403；工作台的「用户管理」入口与面板服务器级配置对普通用户隐藏。
- 修复 `internal/httpapi` 测试因使用 `t.Context()`（需要 go1.24）而无法编译的问题，改为 `context.Background()`；`go vet ./internal/...` 与 `go test ./...` 全部通过。
- 新增 `build-gateway.sh`：一条命令交叉编译 `gateway/yyb-go-{amd64,arm64}`，替代原先手工执行的 go build。
- 新增公开接口访问令牌 `YYB_API_TOKEN`（`internal/httpapi/api_token.go`）：配置后 `/login` 取码分支、`/instances`、`/whoami`、`/wxapp/*`、`/wx/*`、`/openapi.json` 需要 `Authorization: Bearer <token>`、`X-API-Token: <token>` 或 `?token=<token>`，也接受有效的控制台登录会话；留空则完全保持旧行为。`/health`、`POST /login`（带 `username` 的控制台登录）、`GET /login` 登录页、`/register`、`/logout`、`/wxcode/*` 始终放行。令牌比较使用 `crypto/subtle` 常量时间函数。

## 2026-08-26

- 修复 wxcode 兼容取码在多个原生账号时失败的问题：`GET /login?appId=` 无设备 Hook 回退原生取码时，多账号不再直接报 `multiple native accounts configured`，而是自动选用「存活（alive）优先、其次最近更新」的默认账号，并在响应中返回 `openid` 标明实际账号。
- `GET /login?appId=wx...&ref=<openid|id>` 支持显式指定原生账号；`POST /login` 的 `ref` 字段行为不变。

## 2026-08-14

- Magisk v0.1.4 修复 Windows 打包导致 `config.conf.example` 使用 CRLF、`PORT` 被解析为 `8000\r` 而无法启动的问题；启动时会自动修复已有持久化配置，并扩大安装包换行检查范围。
- Magisk 运行时增加可配置 DNS 解析器，默认避开 Android 静态程序无法访问的 `[::1]:53`，修复微信登录二维码域名解析失败。
- Web 用户与会话默认改用持久化 SQLite，首个注册账号自动成为管理员；支持通过 `YYB_AUTH_DRIVER` 切换 MySQL 或关闭认证，并继续兼容旧的 `YYB_AUTH_MYSQL_DSN`。
- 微信 HTTPDNS 无响应或缺少 LongLink 候选时，回退到官方 `longcloud.weixin.com:443`，避免在普通 DNS 和 443 端口可用时直接返回 502。
- `/wx/encryptkey` 改为必须提供目标业务的真实 `payload`，不再发送已知无效的空 `getUserEncryptKey` 请求；同步更新控制台与 OpenAPI。
- 补充文章会话、文章扩展、点赞和业务 `encryptData` 的调用边界，明确兼容路由不会自动推导业务参数。

## 2026-08-13

- 按管理平台架构统一工作台、添加账号、运行管理、用户管理和个人设置，增加固定侧栏、顶栏、当前用户身份和移动端导航。
- 用户新增与密码重置改为完整表单对话框，移除浏览器 `prompt` 交互并增加表单内错误反馈。
- 运行日志改为右侧抽屉，自动刷新时保留当前阅读位置；修复青龙 `/open/logs` 响应超过 2 MB 后被截断并报 `unexpected end of JSON input` 的问题。
- 将 Nginx Basic Auth 替换为应用内登录页面，增加注册、退出、个人设置、管理员用户管理和角色权限；用户及网页登录会话使用 MySQL，微信协议数据继续使用 SQLite。
- 网页管理路由使用哈希 Session Cookie 和登录限速；`/wx/*`、`/wxapp/*` 保持兼容，不要求网页登录。
- 合入 [PR #9](https://github.com/525815266/YYB-Go-Enhanced/pull/9)，增加呆呆面板（daidai-panel）支持、青龙/呆呆统一面板驱动和 GHCR 多架构镜像构建工作流。
- 修复面板适配引入的青龙兼容问题：青龙任务启停继续以 `isDisabled` 判断，运行状态优先使用青龙 `status`，不会因残留 PID 被误判为运行中。
- 呆呆面板删除任务失败时不再忽略错误；增加青龙状态兼容测试，并通过全量测试与静态检查。

## 2026-08-06

- [7408e0b](https://github.com/525815266/YYB-Go-Enhanced/commit/7408e0b) 将二维码授权加入首页“调用配置”，不选账号也可直接创建授权会话。
- [7ec2917](https://github.com/525815266/YYB-Go-Enhanced/commit/7ec2917) 新增截图所示的 `/wx/*` 兼容接口：`/wx/code`、`/wx/getuserinfo`、`/wx/encryptkey`、`/wx/getphonenumber`、`/wx/cloud`、`/wx/qrcodeauth`、`/wx/mpgeta8key`、`/wx/appmsgext` 和 `/wx/appmsglike`；其中云函数及文章相关接口复用 `operateWxData`，不会伪造微信结果。
- [a89cb04](https://github.com/525815266/YYB-Go-Enhanced/commit/a89cb04) 将公众号网页授权并入首页“调用配置”。选择“公众号网页授权”后，可填写公众号 AppID、回调地址、授权范围和 State，并生成官方 OAuth 授权链接。
- [2e09fc5](https://github.com/525815266/YYB-Go-Enhanced/commit/2e09fc5) 新增 `POST /wx/oauth`，校验公众号 AppID、回调地址和授权参数；不伪造授权 code，用户授权后由回调地址接收 code。

## 2026-08-05

- [c2295cb](https://github.com/525815266/YYB-Go-Enhanced/commit/c2295cb) 增加本机微信快速授权，并保留手机扫码作为回退方式。

## 2026-08-04

- [dc74f47](https://github.com/525815266/YYB-Go-Enhanced/commit/dc74f47) 增加青龙一键同步和账号备注，备注可参与账号任务管理。
- [f3e4599](https://github.com/525815266/YYB-Go-Enhanced/commit/f3e4599) 增加账号级联删除，清理对应的 YYB 数据、青龙环境变量和专属任务。

## 2026-08-03

- [47e8fd8](https://github.com/525815266/YYB-Go-Enhanced/commit/47e8fd8) 收录修复后的青龙脚本。
- [c9fb7e7](https://github.com/525815266/YYB-Go-Enhanced/commit/c9fb7e7) 修复 `wx.login` 返回空 code 时的会话重建逻辑。

## 2026-07-31

- [24a1ae8](https://github.com/525815266/YYB-Go-Enhanced/commit/24a1ae8) 增加账号凭据主动保活和提前续期。
- [b462dd5](https://github.com/525815266/YYB-Go-Enhanced/commit/b462dd5)、[2e0b691](https://github.com/525815266/YYB-Go-Enhanced/commit/2e0b691)、[75f4461](https://github.com/525815266/YYB-Go-Enhanced/commit/75f4461) 增加青龙账号级任务运行、开关和日志查询，并修复任务状态显示。
- [afba7fd](https://github.com/525815266/YYB-Go-Enhanced/commit/afba7fd) 修复运行日志刷新时强制跳回顶部的问题，保留当前阅读位置。

## 2026-07-30

- [a0cb5bb](https://github.com/525815266/YYB-Go-Enhanced/commit/a0cb5bb) 发布增强版基础功能：微信扫码登录、账号与 OpenID 管理、`wx.login` code 获取、SQLite 持久化、Docker 部署和青龙接入。

## 当前边界

公众号功能是网页 OAuth 授权链接生成，不是微信公众号后台登录。公众号后台需要其官方管理员登录；OAuth 授权成功后的 `code` 会回调到公众号后台配置的授权域名。
