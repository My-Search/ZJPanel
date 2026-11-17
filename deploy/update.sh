#!/bin/bash
# =============================================================
# ZJPanel 升级/更新脚本（在宿主机执行）
#   ./update.sh              # 升级到最新版（ghcr.io/my-search/zjpanel:latest）
#   ./update.sh v1.2.3       # 升级到指定版本
# 前提：本目录含 docker-compose.yml（面板部署目录）
#
# 也可在面板「设置 → 检查更新」中一键更新：容器部署会改 compose→清理残留→重建时自动拉新镜像
# （版本固化进镜像、down/up 不回退，但站点短暂中断）；本脚本等价地把镜像同步到指定版本。
# 非容器直跑时面板走二进制原地替换（不中断站点）。
# =============================================================
set -e

cd "$(dirname "$0")"
COMPOSE_FILE="docker-compose.yml"
IMAGE="ghcr.io/my-search/zjpanel"

if [ ! -f "$COMPOSE_FILE" ]; then
  echo "[update] 未找到 docker-compose.yml，请在本面板部署目录中执行"
  exit 1
fi

command -v docker >/dev/null 2>&1 || { echo "[update] 未安装 docker"; exit 1; }

VERSION="${1:-latest}"
# 规范化：传 v1.2.3 与 1.2.3 均可（镜像 tag 原样使用）
echo "[update] 目标镜像：${IMAGE}:${VERSION}"

echo "[update] 拉取镜像 ..."
docker pull "${IMAGE}:${VERSION}"

# 将 compose 文件中的镜像替换为目标版本（保持其余配置不变）
sed -i.bak "s#image: ghcr.io/my-search/zjpanel:[^ \"']*#image: ${IMAGE}:${VERSION}#" "$COMPOSE_FILE"

echo "[update] 重建面板容器 ..."
docker compose up -d --remove-orphans || docker-compose up -d --remove-orphans

echo "[update] 完成。等待面板就绪（http://服务器IP:9000）..."
sleep 5
docker compose ps 2>/dev/null || docker-compose ps
