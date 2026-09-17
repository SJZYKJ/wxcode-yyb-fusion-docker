# YYB Go 融合网关（预编译二进制，多架构）
# 构建: docker compose build
#   或  docker build --build-arg TARGETARCH=amd64 .
FROM alpine:3.21

ARG TARGETARCH

RUN apk add --no-cache ca-certificates wget su-exec \
    && addgroup -S yyb && adduser -S -G yyb -h /app yyb

WORKDIR /app

# yyb-go 融合网关二进制（按架构选 amd64/arm64）
COPY gateway/yyb-go-${TARGETARCH} /app/yyb-go

# Web 面板资源
COPY gateway/resource /app/resource

# wxcode APK 随镜像内置（可选：取出安装到已 root 手机作设备取码端点）
COPY wxcode/wxcode_2.1.0.apk /app/wxcode/wxcode_2.1.0.apk

# entrypoint 以 root 启动（修复 bind-mount 数据目录权限）后降权到 yyb 运行
COPY entrypoint.sh /entrypoint.sh

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
