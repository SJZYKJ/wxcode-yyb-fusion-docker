# NOTICE

## 第三方代码与来源

本仓库 `src/` 目录内的服务端源码，整理自以下两个公开仓库：

- [SuperNaiBA/YYB_GO](https://github.com/SuperNaiBA/YYB_GO)（原始应用宝协议服务）
- [525815266/YYB-Go-Enhanced](https://github.com/525815266/YYB-Go-Enhanced)（增强版，本项目主要基于它）

`wxcode/wxcode_2.1.0.apk` 为 wxcode 项目的 Android 客户端，仅作可选设备取码端点随镜像分发。

上述仓库均**未附带开源许可证**。本仓库作者在此声明：本仓库对 `src/` 的使用定位是
个人部署与研究，署名归原作者；如原作者认为本仓库的公开形式不妥，请提 issue，会立即调整或下架。

## 本项目自有内容

除 `src/` 与 `wxcode/` 之外的文件——`Dockerfile`、`Dockerfile.src`、`compose.yaml`、
`compose.build.yaml`、`entrypoint.sh`、`build-gateway.sh`、`.github/workflows/`、
`.env.example`、`gateway/resource/`、`README-DOCKER.md` 及本文件——为本项目自有内容。

本项目在 `src/` 基础上做过的改动，见 `src/CHANGELOG.md` 与 `README-DOCKER.md` 的版本记录，
主要包括：

- **多用户账号隔离**：账号按 `owner_user_id` 归属，普通用户只能看到/管理自己扫码添加的账号，
  并修复普通用户被重定向到个人设置页、无法使用工作台与扫码页的问题；
- **账号级「脚本可读」开关**（`api_shared`）：公开取码接口默认仍可读全部账号，拥有者可单独关闭；
- **公开接口访问令牌**（`YYB_API_TOKEN`）：可选地为取码接口加一层鉴权；
- 修复 `internal/httpapi` 测试因 `t.Context()` 需要 go1.24 而无法编译的问题；
- 补充 `gateway/` 预编译二进制的可复现构建脚本 `build-gateway.sh`。
