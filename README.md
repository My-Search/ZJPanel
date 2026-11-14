# ZJPanel

Go + Vue3 + TypeScript 服务器管理面板：**nginx 管理 + SSH 终端 + Docker 容器管理 + MCP + 证书自动化（certd 式管线）+ 插件化扩展**，单容器 Docker 部署。

## 功能一览

| 模块 | 说明 |
|---|---|
| **网站** | sites-available/enabled 模式（nginx-ui 同款），反代/静态站点，conf 生成 + 高级模式 raw 编辑；保存先 `nginx -t` 再 reload，失败自动回滚 |
| **维护页** | 全局模板 + 站点级覆盖；开启维护生成 `sites-enabled/<域名>_maintenance.conf`，请求代理到面板渲染器 |
| **默认页** | 404 / 50x / 首页 HTML 可视化编辑（Nginx → html 目录） |
| **Compose 导入** | 「应用 → 导入 docker-compose 应用」文件夹选择向导（宿主机目录默认映射 `/opt:/opt`，容器内路径=宿主机路径），解析服务与发布端口导入为反代站点；站点声明写入目录下 `zjpanel.json`，面板变化自动写回，迁移时扫目录即恢复 |
| **终端** | SSH 主机管理（密码/私钥）+ 本地终端；会话常驻后端（环形缓冲），**页面刷新自动恢复所有标签并回放**，多会话并发 |
| **Docker** | 容器/镜像/网络/卷管理，日志跟随、exec 终端、镜像拉取（WS 进度） |
| **证书** | certd 式管线：申请（HTTP-01/DNS-01，lego 多 DNS 提供商）→ 部署目标（本机 nginx / SSH 上传 / 本地脚本，可插件扩展）→ cron 自动续期；证书仓库 + 运行历史实时日志 |
| **请求地图** | access log 增量解析 + GeoLite2 城市级聚合（城市/国家/坐标），ECharts 世界地图 + 城市散点 + 流向线，WS 实时刷新 |
| **监控** | CPU/内存/负载/网络实时（WS）+ 磁盘用量；历史采样 30 天 |
| **MCP** | 面板唯一 `/mcp` 端点（Streamable HTTP + API Key），核心工具与**插件声明的工具聚合**统一对外 |
| **插件** | 内置/外部插件同一 SDK：菜单页、HTTP 路由、cron、事件订阅、MCP 工具、证书 DNS 提供商与部署目标注册；zip 上传安装，Yaegi 解释执行 |
| **更新** | `update.sh`（宿主机执行）+ 面板一键更新（经一次性 docker:cli 容器执行 compose pull && up -d，重建自身不中断流程） |

## 快速部署（Linux 服务器）

```bash
mkdir -p /opt/zjpanel && cd /opt/zjpanel
# 下载（或复制仓库中的）deploy/docker-compose.yml 与 deploy/update.sh
cp docker-compose.yml update.sh ./
docker compose up -d
```

访问 `http://服务器IP:9000` → 首次进入创建管理员账号。

镜像来自 GitHub Container Registry：`ghcr.io/my-search/zjpanel`（由 GitHub Actions 在推送 `v*` tag 时自动构建 amd64/arm64 并发布 Release）。

**应用目录映射**：默认 `/opt:/opt`（宿主机 ↔ 容器，路径一致）。把 docker-compose 应用放到宿主机 `/opt` 下的任意目录，在面板「应用 → 导入 docker-compose 应用」中即可选择导入；如需导入其他宿主机目录，在 `docker-compose.yml` 的 volumes 中追加一行 `- /srv/apps:/srv/apps` 后执行 `./update.sh` 重建。

目录结构（均在部署目录下，容器重建不丢）：

```
/opt/zjpanel/
├── docker-compose.yml / update.sh
├── data/          # SQLite、证书、插件、geoip、维护页模板
├── nginx/conf/    # nginx 配置（sites-available/enabled、nginx.conf）
├── nginx/logs/    # 访问/错误日志（请求地图数据源）
├── nginx/html/    # 默认页 + 静态站点目录
└── compose/       # docker-compose 项目目录（在此放你的 compose 项目）
```

> **host 网络说明**：容器使用 `network_mode: host`（Linux），nginx 直接监听宿主机 80/443，面板 9000；反向代理 compose 服务直接 `127.0.0.1:<发布端口>`。

## 升级

```bash
./update.sh            # 最新版（ghcr.io/my-search/zjpanel:latest）
./update.sh v1.2.3     # 指定版本
```

或在面板「设置 → 检查更新」中一键更新（面板经一次性 updater 容器执行 `docker compose pull && up -d`，面板容器自身被替换也不中断）。版本清单读取 GitHub Release（`api.github.com/repos/My-Search/ZJPanel/releases/latest`），推 `v*` tag 即发布新版本。

## 本地开发

```bash
# 后端（Windows 本机无 nginx/docker 时自动 dev 模式：写配置跳过 test/reload、Docker 显示不可用）
cd backend && go run ./cmd/zjpanel          # 端口 9000，数据在 ./dev-data

# 前端（dev 代理到 9000）
cd frontend && npm run dev                  # 端口 5173

# 构建产物（前端 dist 直接输出到 backend embed 目录）
cd frontend && npm run build
cd backend && go build -o zjpanel ./cmd/zjpanel
```

## MCP 接入

面板「MCP」页生成 API Key，客户端配置：

```json
{ "mcpServers": { "zjpanel": { "url": "http://面板IP:9000/mcp", "headers": { "X-Api-Key": "<key>" } } } }
```

内置工具：`panel_info / list_sites / create_proxy_site / toggle_site / nginx_test_reload / list_containers / container_action / container_recent_logs / get_metrics / list_cert_tasks / run_cert_task / compose_projects / compose_action`；插件可注册更多（命名 `<插件id>_<工具名>`）。

## 插件开发

外部插件 = `plugin.yaml + main.go`（可含 `dist/` 前端页），打成 zip 在「插件」页安装：

```yaml
# plugin.yaml
id: my-plugin
name: 我的插件
version: 0.1.0
menu:
  - title: 我的页面
    path: /
```

```go
// main.go（Yaegi 解释执行，可用全部标准库）
package main

import sdk "zjpanel/backend/sdk/plugin"

type P struct{ ctx sdk.Context }

func New() sdk.Plugin { return &P{} }

func (p *P) Meta() sdk.Meta {
	return sdk.Meta{ID: "my-plugin", Name: "我的插件", Version: "0.1.0",
		Menu: []sdk.Menu{{Title: "我的页面", Path: "/"}}}
}
func (p *P) OnInit(ctx sdk.Context) error {
	p.ctx = ctx
	ctx.RegisterMCPTool("hello", "打个招呼",
		map[string]any{"type": "object", "properties": map[string]any{
			"name": map[string]any{"type": "string"}}},
		func(args map[string]any) (string, error) { return "hello " + args["name"].(string), nil })
	ctx.SubscribeEvent("sitecheck.down", func(payload map[string]any) { /* 告警 */ })
	return nil
}
func (p *P) OnShutdown() {}
```

参考实现：`backend/plugins/notifywebhook/`（Webhook 通知：事件订阅 + MCP 工具 + 声明式设置）。

## 证书自动续期原理（certd 式）

每个任务按 cron 每日触发；`SkipWhenSucceed`：距到期 > 续期窗口（默认 20 天）直接跳过复用上次结果；临近到期自动重新申请并**并行**执行全部部署目标（写入本机 nginx 证书目录并 reload / SFTP 上传到远程主机并执行后置命令 / 自定义脚本），全过程按节点记录日志并在 UI 实时展示。HTTP-01 由 nginx 将 `/.well-known/acme-challenge` 转发到面板挑战端口（9100）；DNS-01 经 lego 支持 Cloudflare/阿里/腾讯/华为等，也可由插件注册新提供商。

## 目录结构

```
ZJPanel/
├── Dockerfile              # 三阶段：前端 → Go embed → alpine(nginx+docker cli)
├── deploy/
│   ├── docker-compose.yml  # 单容器部署（host 网络）
│   ├── update.sh           # 宿主机升级脚本
│   ├── entrypoint.sh       # 初始化 nginx → 启动面板
│   └── nginx-defaults/     # 默认 nginx.conf / 默认站点 / 默认页
├── backend/
│   ├── cmd/zjpanel/
│   ├── sdk/plugin/         # 插件 SDK（稳定接口）
│   ├── internal/           # boot/nginx/site/compose/cert/terminal/docker/metrics/logmap/mcp/plugin/update/http
│   └── plugins/notifywebhook/  # 内置示例插件
└── frontend/               # Vue3 + TS + Ant Design Vue
```

## 安全说明

- 面板为单管理员模型，登录会话 HttpOnly Cookie；登录限速（8 次/分钟/IP）
- SSH 密码/私钥、ACME 账户私钥、DNS 凭证均 AES-GCM 加密落库（主密钥 `data/secret.key`）
- MCP API Key 仅存哈希；API Key 只能访问 `/mcp`，不能访问面板 UI 与 WS
- 全部变更操作记录审计日志（设置 → 审计）
