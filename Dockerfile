# syntax=docker/dockerfile:1
# ========= 阶段 1：前端构建（固定构建机原生平台；产物为静态文件与架构无关，amd64/arm64 共用一份） =========
FROM --platform=$BUILDPLATFORM node:22-alpine AS fe
WORKDIR /fe
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY frontend/ ./
# 世界地图数据（构建时下载，产物随镜像分发）
RUN mkdir -p public/map && \
    (curl -sL -o public/map/world.json https://testingcf.jsdelivr.net/npm/echarts@4.9.0/map/json/world.json || \
     curl -sL -o public/map/world.json https://unpkg.com/echarts@4.9.0/map/json/world.json || true)
RUN [ -f public/map/world.json ] || echo '{"type":"FeatureCollection","features":[]}' > public/map/world.json
# 输出到后端 embed 目录
RUN npx vue-tsc -b || true && npx vite build --outDir /fe/dist

# ========= 阶段 2：后端构建（原生平台交叉编译，CGO 关闭纯静态，不走 QEMU） =========
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS be
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64}
COPY backend/go.mod backend/go.sum ./
ENV GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct
RUN go mod download
COPY backend/ ./
# 用前端产物覆盖占位 dist（跨阶段必须用 COPY --from，RUN 内看不到 fe 阶段文件）
RUN rm -rf internal/http/dist
COPY --from=fe /fe/dist ./internal/http/dist
ARG VERSION=dev
RUN go build -ldflags "-s -w -X zjpanel/backend/internal/buildinfo.Version=${VERSION}" -o /out/zjpanel ./cmd/zjpanel
# 主机 Agent：与面板同版本构建，随镜像分发（runtime 按 TARGETARCH 命名）
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-amd64} go build -ldflags "-s -w -X zjpanel/backend/internal/buildinfo.Version=${VERSION}" -o /out/zjagent ./cmd/zjagent

# ========= 阶段 3：运行层（nginx + 面板 + docker cli，单容器） =========
FROM alpine:3.20
# git 供 Webhooks 插件在容器内执行 git pull 部署
RUN apk add --no-cache nginx docker-cli docker-cli-compose bash curl git ca-certificates tzdata s6-overlay 2>/dev/null || \
    apk add --no-cache nginx docker-cli docker-cli-compose bash curl git ca-certificates tzdata
ENV TZ=Asia/Shanghai
ARG TARGETARCH=amd64
COPY --from=be /out/zjpanel /app/zjpanel
COPY --from=be /out/zjagent /tmp/zjagent
RUN mkdir -p /app/agent && mv /tmp/zjagent /app/agent/zjagent-linux-${TARGETARCH} && chmod +x /app/agent/zjagent-linux-${TARGETARCH}
COPY deploy/entrypoint.sh /app/entrypoint.sh
COPY deploy/nginx-defaults/ /app/nginx-defaults/
COPY backend/plugins/notifywebhook/ /app/plugin-examples/notifywebhook/
# 内置插件前端资产（/plugin-assets/<id>/ 从此读取）
COPY plugin-assets/ /app/plugin-assets/
RUN chmod +x /app/entrypoint.sh /app/zjpanel && \
    mkdir -p /etc/nginx/sites-available /etc/nginx/sites-enabled /etc/nginx/conf.d /var/log/nginx /usr/share/nginx/html /data /compose
EXPOSE 80 443 9000
ENTRYPOINT ["/app/entrypoint.sh"]
CMD ["nginx", "-g", "daemon off;"]
