# ========= 阶段 1：前端构建 =========
FROM node:22-alpine AS fe
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

# ========= 阶段 2：后端构建（CGO 关闭，纯静态） =========
FROM golang:1.27-alpine AS be
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
ENV GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct
RUN go mod download
COPY backend/ ./
# 用前端产物覆盖占位 dist
RUN rm -rf internal/http/dist && mkdir -p internal/http/dist && \
    cp -r /fe/dist/* internal/http/dist/
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w -X zjpanel/backend/internal/buildinfo.Version=${VERSION}" -o /out/zjpanel ./cmd/zjpanel

# ========= 阶段 3：运行层（nginx + 面板 + docker cli，单容器） =========
FROM alpine:3.20
RUN apk add --no-cache nginx docker-cli docker-cli-compose bash curl ca-certificates tzdata s6-overlay 2>/dev/null || \
    apk add --no-cache nginx docker-cli docker-cli-compose bash curl ca-certificates tzdata
ENV TZ=Asia/Shanghai
COPY --from=be /out/zjpanel /app/zjpanel
COPY deploy/entrypoint.sh /app/entrypoint.sh
COPY deploy/nginx-defaults/ /app/nginx-defaults/
COPY backend/plugins/notifywebhook/ /app/plugin-examples/notifywebhook/
RUN chmod +x /app/entrypoint.sh /app/zjpanel && \
    mkdir -p /etc/nginx/sites-available /etc/nginx/sites-enabled /etc/nginx/conf.d /var/log/nginx /usr/share/nginx/html /data /compose
EXPOSE 80 443 9000
ENTRYPOINT ["/app/entrypoint.sh"]
CMD ["nginx", "-g", "daemon off;"]
