#!/bin/sh
# ZJPanel 单容器入口：初始化 nginx 配置 → 启动 nginx → 启动面板（前台）
set -e

NGINX_PREFIX="${ZJPANEL_NGINX_PREFIX:-/etc/nginx}"
HTML_DIR="${ZJPANEL_NGINX_HTML:-/usr/share/nginx/html}"
LOG_DIR="${ZJPANEL_NGINX_LOGS:-/var/log/nginx}"

# ---- 首次启动：写入默认 nginx.conf / 默认站点 / 默认页 ----
if [ ! -f "$NGINX_PREFIX/nginx.conf" ]; then
  echo "[entrypoint] 初始化 nginx.conf ..."
  mkdir -p "$NGINX_PREFIX/sites-available" "$NGINX_PREFIX/sites-enabled" "$NGINX_PREFIX/conf.d" \
           "$NGINX_PREFIX/ssl" "$LOG_DIR" "$HTML_DIR"
  cp /app/nginx-defaults/nginx.conf "$NGINX_PREFIX/nginx.conf"
  cp /app/nginx-defaults/conf.d/default.conf "$NGINX_PREFIX/conf.d/default.conf"
  # 默认页由面板（site.EnsureDefaults）补齐；这里先放占位
  [ -f "$HTML_DIR/index.html" ] || cp /app/nginx-defaults/html/index.html "$HTML_DIR/index.html"
  [ -f "$HTML_DIR/404.html" ] || cp /app/nginx-defaults/html/404.html "$HTML_DIR/404.html"
  [ -f "$HTML_DIR/50x.html" ] || cp /app/nginx-defaults/html/50x.html "$HTML_DIR/50x.html"
fi

# ---- 启动 nginx（后台，daemon off 由 s6/手动管理） ----
echo "[entrypoint] 启动 nginx ..."
nginx -t && nginx || { echo "[entrypoint] nginx 启动失败"; exit 1; }

# nginx 挂掉时拉起（简单守护）
(
  while true; do
    sleep 5
    pidof nginx >/dev/null 2>&1 || { echo "[entrypoint] nginx 退出，重新拉起"; nginx; }
  done
) &

# ---- 启动面板（前台） ----
echo "[entrypoint] 启动 ZJPanel ..."
exec /app/zjpanel
