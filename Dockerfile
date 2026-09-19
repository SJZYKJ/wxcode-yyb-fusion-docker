# YYB Go 融合网关（预编译二进制，多架构）
# 构建: docker compose build
#   或  docker build --build-arg TARGETARCH=amd64 .
FROM alpine:3.21

ARG TARGETARCH

# 本次发布的版本号（如 v7），由 CI 用 --build-arg VERSION=v7 传入；
# 本地自建不传就是 dev。写入镜像内的 /static/version.json，
# 登录页读取它显示「当前版本」，所以以后发版不用再改登录页。
ARG VERSION=dev

RUN apk add --no-cache ca-certificates wget su-exec \
    && addgroup -S yyb && adduser -S -G yyb -h /app yyb

WORKDIR /app

# yyb-go 融合网关二进制（按架构选 amd64/arm64）
COPY gateway/yyb-go-${TARGETARCH} /app/yyb-go

# Web 面板资源
COPY gateway/resource /app/resource

# entrypoint 以 root 启动（修复 bind-mount 数据目录权限）后降权到 yyb 运行
COPY entrypoint.sh /entrypoint.sh

# 登录页读取的版本号（公开静态文件，无需鉴权）。
# 放在下方的 chmod 之前，好让它与其它静态资源一样被统一成 644。
# sed 只保留 [A-Za-z0-9._-]，保证写出来的一定是合法 JSON 值。
RUN printf '{"version":"%s"}\n' \
      "$(printf '%s' "${VERSION}" | sed 's/[^A-Za-z0-9._-]//g')" \
      > /app/resource/static/version.json \
    && cat /app/resource/static/version.json

# 数据目录（db/avatars/qr 用卷挂载）；entrypoint 会再修复 bind-mount 权限
RUN mkdir -p /app/resource/db /app/resource/avatars /app/resource/qr \
    && chown -R yyb:yyb /app \
    && find /app -type d -exec chmod 755 {} + \
    && find /app -type f -exec chmod 644 {} + \
    && chmod 755 /app/yyb-go /entrypoint.sh

USER root
EXPOSE 8088

ENTRYPOINT ["/entrypoint.sh"]
CMD ["-host", "0.0.0.0", "-port", "8088", "-resource-root", "/app/resource"]
