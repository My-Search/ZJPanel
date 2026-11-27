# 主机管理 Agent 设计（P0 技术方案）

> 目标：把「一台能 SSH 登进去的机器」升级为「一台能持续管理、持续防护的机器」。
> 定位：SSH 是引导通道（bootstrap），Agent 是常驻控制面，主机页是操作台，MCP 是对外接口。

## 1. 边界

**做**：主机清单统一、Agent 安装/更新/卸载、心跳与基础指标、主机防御动作（SSH 加固、账号与持久化检查、端口暴露、封禁）、事件统一、MCP 工具。

**不做**：通用监控（历史曲线/告警阈值/图表缩放）、通用远程执行、eBPF 级 EDR、跨平台（先只支持 Linux + systemd，amd64/arm64）。

## 2. 总体架构

```
浏览器 ──HTTP/WS──> 面板（控制面：策略、审批、审计、UI）
                     │  ▲
                     │  │ 反向 WebSocket（Agent 出站，无需入站端口）
                     ▼  │
                  Agent（宿主侧：采集 + 本地判定 + 受限执行）
                     │
                     ├─ /var/log/auth.log | /var/log/secure | journald
                     ├─ nftables（封禁）
                     ├─ /proc、ss、systemd
                     └─ 本地 spool（断网自治 + 补传）
```

三条硬原则：

1. **检测在 Agent，策略在面板**。断网时 Agent 按最后一份策略继续工作，事件落本地队列，恢复后补传。面板永远不做实时判定。
2. **控制通道与交互式终端分离**。PTY 只服务人，Agent 通道只服务机器。
3. **Agent 只暴露结构化动作白名单**，永不提供通用 exec。

## 3. Agent 设计（主流形态）

单静态二进制 `zjagent`，子命令式：

| 子命令 | 作用 |
|---|---|
| `install`（默认） | 探测环境 → 落盘 → 装 unit → 启动 → 回连确认 |
| `run` | 前台运行（调试 / 非 systemd 环境） |
| `uninstall [--purge]` | 停 unit、删 unit、删目录，可选清空 spool/配置 |
| `version` | 打印版本与协议版本 |
| `doctor` | 自检：权限、systemd、nftables、日志源、出网、时钟偏移 |
| `update` | 手动触发面板下发式更新（等价于面板按钮的本地版） |

运行参数：

- unit：`zjagent.service`，`Restart=always`，`RestartSec=5`，`LimitNOFILE=65535`
- 配置 `/etc/zjagent/agent.yaml`（0600）：面板地址列表、采集周期、spool 上限、日志级别
- token `/etc/zjagent/token`（0600，单独文件，便于轮换）
- 状态/spool `/var/lib/zjagent/`
- 版本双槽：`/usr/local/lib/zjagent/versions/<ver>/zjagent`，`/usr/local/bin/zjagent` 为 symlink，升级只切 symlink（原子、可秒回滚）
- 资源目标：常态 CPU < 1%、RSS < 50MB；采集周期默认 10s，指标聚合后上报 30s

通信协议（JSON over WSS，Agent 出站）：

```
agent → panel  hello   {agentId, version, protocol, os, arch, kernel, hostname, bootAt, clockSkew}
panel → agent  welcome {hostId, policyVersion, policy, serverTime}
agent → panel  heartbeat {ts, load, mem, disk, conns, sshSessions, failRate}
agent → panel  eventBatch [{seq, layer, kind, severity, ip, detail, ts, count}]
panel → agent  cmd     {id, kind, params, deadline}
agent → panel  cmdResult {id, status, output, error, startedAt, finishedAt}
agent → panel  metric  {ts, series...}
```

- 指令幂等：`cmd.id` 全局唯一，Agent 本地持久化已执行 ID（保留 24h），重复下发直接回放结果。
- 事件补传：`eventBatch` 带单调 `seq`，面板按 `seq` 去重，Agent 收到 ack 才推进游标。
- spool 上限默认 200MB，超限按 LRU 丢最旧事件并把丢弃数量作为指标上报（不静默丢）。
- 本地熔断：连续 3 次高危指令失败或凭据失效 → 进入只读模式（只上报，不执行），需面板重新授权。

## 4. 安装幂等与更新机制（对应需求 1）

**幂等**：安装脚本是「收敛」而非「一次性执行」。

```
探测（OS/arch/libc/systemd/root/curl|wget/出网/时钟）
  → 已装且版本一致且配置一致  → 直接回连确认，退出 0
  → 已装且版本不同            → 走升级路径（双槽切换）
  → 未装                      → 下载 + 校验 sha256 + 安装 + 启动
  → 任一步失败                → 明确报错退出，不留半装状态（失败回滚）
```

非 root 且 sudo 需要密码时，**执行前就报错退出**，不做“装一半”。

**更新感知**（三层，主次明确）：

1. **握手协商（主）**：Agent 每次重连上报 `version` + `protocol`，面板比对 → 标记「落后 N 个版本 / 协议不兼容」。离线机器也能在面板看到“该升级”。
2. **面板下发（执行）**：`upgrade` 指令携带目标版本 + 下载地址 + 签名；Agent 下载校验 → 驻留新槽 → 切 symlink → 重启 → 新进程自检通过并回报；失败由 systemd 拉起旧槽（`previous` symlink 保留）。
3. **Agent 定时自检（兜底）**：每 1h ±10min 抖动检查一次（可关），只上报不自动执行。

**面板展示**：

- 主机列表：`v0.3.2 · 面板 v0.4.0 · 落后`，落后项单独高亮。
- 主机详情：Agent 版本、协议版本、上次升级时间、升级结果。
- 操作：单台「升级」、批量「升级选中」、`升级失败自动回滚` 开关（默认开）。
- 灰度：批量升级默认分批（每批 ≤ 10% 或 ≤ 5 台），单台失败即暂停后续。

## 5. 认证与连接（对应需求 3）

- **两级 token**：`bootstrap token`（面板生成，30 分钟有效、单次使用、绑定创建时的 IP 可选）→ 换取 `host token`（长期、按 host 存储、可轮换、可吊销、记录最后使用时间）。
- 存储：复用 `pkg.SecretBox` 的 ctx 加密，不明文落库。
- **多地址回退**：面板地址列表支持「主域名 → 备用 IP → 备用端口」，Agent 依次尝试；失败原因分类回显（DNS / TLS / 超时 / 401 / 协议不兼容）。
- **TLS**：优先域名证书；IP 直连时用面板证书指纹固定（注入时下发，Agent 校验，防中间人）。
- 面板侧独立 `agent.Registry`（`hostID → conn`），心跳 45s 无消息判离线，指令排队 + 超时（默认 30s）+ 重试（3 次）。

## 6. 终端与主机合并（对应需求 2）

**决策：合并。** 理由：一台机器只应有一个「对象」，凭据、Agent、终端、防御都挂在它身上，避免两套主机清单。

进入主机列表的两条路径：

| 路径 | 流程 |
|---|---|
| **SSH 添加** | 填 host/port/user/凭据 → 测试连接 → 勾选「立即安装 Agent」（默认勾选）→ 注入并回连确认 |
| **命令安装** | 面板生成一行命令（含 bootstrap token + 面板地址）→ 目标机执行 → Agent 回连 → 出现在「待认领」→ 认领后并入列表 |

终端能力的落位：

- 每台主机详情内 **「终端」Tab**，复用现有 PTY + WS 实现（`terminal.Session` 不动）。
- 内置伪主机 **「本机」**（`local`）：永远在线、不可卸载、隐藏 SSH/Agent 相关 Tab，承接原「容器内 Shell」能力。
- 「临时连接」（不保存凭据的 ad-hoc SSH）放在主机页顶部入口，不占左侧菜单。
- 路由：`/terminal` 保留并重定向到 `/hosts?connect=adhoc`，旧深链不失效。

左侧菜单最终：`仪表盘 / 应用 / Nginx / 主机 / 证书 / 监控 / WAF / MCP / 插件 / 设置`（去掉「终端」，「安全」→「WAF」）。

## 7. 主机页信息架构（对应需求 4）

左侧列表（master）：状态点（在线 / 离线 / 待认领 / 未装 Agent / 有告警）、分组、搜索、按告警数排序。

右侧详情（detail）Tabs：

1. **概览**：健康分、负载/内存/磁盘、监听端口数、SSH 会话、最近事件、Agent 版本。
2. **防御**：检查项列表（SSH 加固 / 账号异常 / 持久化点 / 端口暴露 / webshell），每项显示「上次检查时间 / 风险数 / 是否可自动修复」。
3. **进程·端口·连接**：三张表，支持按进程名/端口过滤。
4. **事件**：该主机时间线（与 WAF 事件同源，带 layer 标签）。
5. **终端**。
6. **设置**：策略覆盖、升级、卸载、删除记录。

防御动作统一四段式：**检查（dry-run）→ 结果预览（含影响面）→ 应用 → 结果/回滚**。四项检查共用同一套交互组件，不做四种花样。

## 8. 卸载（对应需求 6）

原则：**只有确认卸载成功才删记录。**

| 路径 | 流程 | 面板状态 |
|---|---|---|
| 面板下发 | `uninstall` 指令 → Agent 停 unit/删文件 → 回报 `uninstalled` → 面板删除记录 | 已卸载（自动删） |
| 本机命令 | `zjagent uninstall --purge` → 尽力先上报 `uninstalling` → 本地清理 | 心跳超时后标「疑似已卸载」，用户手动确认删除 |
| 离线强制 | 面板「强制移除记录」 | 标「未确认移除」，记录保留 30 天，不假装成功 |

## 9. 封禁中心（全局实体）

WAF（HTTP，nginx `geo`）与主机（全端口/SSH，nftables）都要封 IP，**必须单一数据源**：

```
Ban { value(IP/CIDR), scope(http|host|both), source(waf|host|manual|ai),
      reason, until, createdBy, createdAt, hits }
```

- WAF 页与主机页都是它的视图：WAF 页只看 `scope ∈ {http, both}`，主机页只看 `host/both`。
- `both` 触发两个执行器（渲染 nginx + agent 下发），任一失败要显式提示「部分生效」。
- 解封也从这一个入口走，避免“WAF 解了主机还封着”。

## 10. MCP 工具

命名 `host_*`，与 HTTP API 共用同一个 service 方法与同一套 audit，不为 MCP 开旁路。

| 工具 | 权限 |
|---|---|
| `host_list` / `host_get` / `host_metrics` / `host_ports` / `host_events` / `host_processes` / `host_ssh_sessions` / `host_check_list` | 只读 |
| `host_check_run`（dry-run 检查） | 只读副作用（不改系统） |
| `host_install_agent` / `host_uninstall_agent` / `host_upgrade_agent` | 写，AI 默认不给 |
| `host_block_ip` / `host_apply_sshd` / `host_lock_user` / `host_scan_webshell` | 写，必须人在环 |

## 11. 数据模型

```go
// Host 合并原 SshHost（保留表名，增量加字段）
Host { ID, Name, Addr, Port, Username, AuthType, PasswordEnc, PrivateKeyEnc, PassphraseEnc,
       Group, Tags, AgentID, AgentStatus, AgentVersion, Protocol, LastSeen, OSArch, CreatedAt }

HostAgent { HostID, AgentID(uuid), TokenEnc, Version, Protocol, Status(pending|online|offline|uninstalled|pending_claim),
            RemoteIP, BootAt, LastSeen, UpdatedAt }

AgentCommand { ID(uuid), HostID, Kind, Params(json), Status(queued|sent|ok|failed|timeout),
               Attempts, Output, Error, CreatedAt, AckedAt }

HostCheck { ID, HostID, Kind, Status, Findings(json), Risk, AppliedAt, RollbackRef }

Event { ...现有字段, HostID, Layer(l7|l4|host) }   // 与 security.Event 同构
```

## 12. 接口清单

主机：`GET/POST /api/hosts`、`PUT/DELETE /api/hosts/:id`、`POST /api/hosts/:id/test`

Agent：`POST /api/hosts/:id/agent/install|uninstall|upgrade`、`GET /api/hosts/:id/agent/doctor`

防御：`POST /api/hosts/:id/check/:kind`（ssh/accounts/persistence/exposure/webshell，只读）、`POST /api/hosts/:id/apply`、`POST /api/hosts/:id/rollback`（规划）

主机层封禁：`GET /api/hosts/:id/firewall`、`POST /api/hosts/:id/bans`（body: ip/seconds）、`DELETE /api/hosts/:id/bans/:ip`

数据：`GET /api/hosts/:id/metrics|events|commands`

Agent 侧：`GET /api/agent/time|install.sh|bin/:arch`、`WS /api/agent/ws`、`POST /api/agent/register`

## 13. 分期

- **P0（3~4 天）**：Host 模型合并 + bootstrap（SSH 注入 / 命令安装）+ Agent 握手/心跳/指标 + 版本展示与升级 + 卸载三路径 + 主机页列表与详情 + 菜单调整（终端并入、安全改 WAF）。
- **P1（4~5 天）**：SSH 加固、账号/持久化检查、端口暴露、事件接入、封禁中心重构、nftables 执行器。
- **P2（3~4 天）**：MCP 只读工具、批量注入、灰度升级、主机列表分组与告警排序。
- **P3**：webshell 扫描、进程/连接异常、暴露面体检、AI 编排。

## 14. 风险

1. **面板是单点**：所有 host token 在面板库。必须做 Agent 本地熔断 + 高危动作二次确认 + 面板自身 2FA。
2. **注入脚本的信任问题**：面板必须能展示「将执行的完整脚本 + sha256」，用户能审阅才敢点。
3. **时钟偏移**：日志判定依赖时间，`doctor` 检查偏移 > 30s 直接告警。
4. **升级翻车**：双槽 + 自动回滚 + 分批，禁止一次性全量。
5. **凭据**：建议提供「仅注入时临时使用，不长期保存」选项，并审计凭据使用。

## 15. 实施状态（P0，2026-11-26）

### 已落地

| 模块 | 实现 | 代码位置 |
|---|---|---|
| 数据模型 | `SshHost` 扩展 group/tags/local/agentId/agentStatus/agentVersion/agentProto/agentLastSeen/remoteIp/osArch/tokenHash/claimed；新增 `AgentCommand`；AutoMigrate | `backend/internal/model/model.go`、`boot/db.go` |
| 控制面 | 两级 token（bootstrap 内存 30min / host token sha256 落库）、连接注册表、心跳、指标与事件环形缓存、指令队列（DB 持久化 + 30s 超时 + 重连补发）、卸载 ack | `backend/internal/agent/agent.go` |
| 安装 | 脚本由面板生成：幂等收敛、root/systemd/架构检测、sha256 校验、双槽目录 + symlink、systemd unit；SSH 注入前先 `uname -m` 探测 arch | `backend/internal/agent/install.go`、`http/routes_hosts.go` |
| Agent 端 | `zjagent` 单二进制：run/install/uninstall/upgrade/doctor/version；15s 心跳；采集负载/内存/磁盘/CPU/连接/SSH 会话/端口；SSH 基线只读审计；双槽升级自替换；断网 spool | `backend/cmd/zjagent/`（`sys_unix.go`/`sys_other.go` 分平台） |
| API | hosts CRUD、test/claim、agent install/uninstall/upgrade、metrics/events/commands、check/ssh；Agent 侧 `time`/`install.sh`/`bin/:arch`/`ws`/`register` | `backend/internal/http/routes_hosts.go` |
| MCP | `host_list`、`host_metrics`、`host_events`、`host_check_ssh`（均只读） | `backend/internal/http/routes_mcp.go` |
| 前端 | 新增主机页（左列表右详情，Tab：概览/防御/事件/终端/设置；两种添加入口；安装输出弹窗；升级/卸载/删除）；终端页支持 `?host=`/`?local=1` 与「装 Agent」；菜单去终端、加主机、安全→WAF | `frontend/src/views/HostsPage.vue`、`TerminalPage.vue`、`SideMenu.vue`、`router/index.ts` |
| 终端内嵌 | 主机页「终端」Tab 不再跳转：内嵌单会话终端（复用 `/api/ws/terminal`、create/attach、二进制帧、5s stats），底部 `StatusStrip` 显示资源，右侧 `TermFilePanel` 负责上传/下载/在线编辑/压缩解压；「多标签终端」仍跳 `/terminal?host=` | `frontend/src/components/HostTerminal.vue`、`TermFilePanel.vue` |
| 分发 | Dockerfile 构建 `zjagent` 并按 TARGETARCH 落到 `/app/agent/zjagent-linux-*`；`BinaryPath` 兼容 `AppDir/agent`、`DataDir/agent` | `Dockerfile`、`internal/agent/install.go` |

### 验证记录

- `go build ./...`、`npm run build`、`go test ./internal/agent/ ./cmd/zjagent/` 全部通过。
- 端到端（Windows 本机）：`/api/agent/register` 生成 bootstrap token → `zjagent run` 出站连 `/api/agent/ws` → 首帧 `hello` 认证 → `welcome` 下发长期 token（日志「已换取长期 token」）→ 面板主机状态 `online`、`agentProto=1`、`osArch=windows/amd64`、`lastSeen` 更新。
- 指令闭环：`POST /api/hosts/:id/check/ssh` 下发 `ssh_audit` → Agent 回 `cmdResult` → 事件流出现「指令完成：ssh_audit」，失败原因（Windows 无 `/etc/ssh/sshd_config`）如实回传。
- 在线态：`Online` 同时要求连接存在且 60s 内有 hello/heartbeat；概览每 5s 轮询指标与主机列表，下线事件推送后立即刷新，避免半开连接导致「永远在线、数字不动」。

### 与文档的偏差（有意）

1. **认证走首帧 hello，不走 query token**：避免 token 落入 nginx/面板访问日志；`?token=` 仍兼容（脚本自检）。
2. **不新建 Host 表**：直接扩展 `SshHost`，一台机器一个对象，避免双清单迁移。
3. 二进制下载路径为 `/api/agent/bin/:arch`（单平台维度），非 `{os}/{arch}`。
4. 「版本落后」用 `agentVersion != buildinfo.Version` 判定，够用且不引入 semver 依赖；灰度/分批升级未做。
5. 终端页路由 `/terminal` 保留（主机页以 `?host=` 调用），旧深链不失效；菜单入口已收进「主机」。

### 后续（P1+，2026-11-27 已补第一版）

v1.0.29 起 defense 后续能力落地，仍坚持「检查只读、动作带 TTL、独立表不碰系统规则」：

| 能力 | 实现 | 代码位置 |
|---|---|---|
| 账号基线 | passwd 中非 root 的 UID 0、空密码、shadow 权限、sudoers NOPASSWD、authorized_keys 权限过宽 | `backend/cmd/zjagent/defense.go` |
| 持久化点 | /etc/crontab、cron.d、用户 crontab、profile.d、rc.local、ld.so.preload、systemd 单元（近 7 天变更 + ExecStart 可疑命令） | 同上 |
| 暴露面 | 0.0.0.0 监听端口、21/23/2375/3306/5432/6379/9200/11211/27017/5900 高危端口、nftables/ufw/firewalld/iptables 生效状态 | 同上 |
| Webshell | /var/www、nginx 默认目录、/home、/opt 下 php/jsp/asp 特征扫描（eval/assert/命令执行/base64/ProcessStartInfo），20s、3 万文件上限 | 同上 |
| 主机层封禁 | 独立表 `inet zjpanel` + timeout set + input drop；仅 IPv4、TTL 60s~7d、到期自动解封；不修改用户既有防火墙规则 | `defense.go`、`routes_hosts.go` |
| 前端 | 防御页五张检查卡 + 结果表（按类型过滤）+ 防火墙状态 + 封禁/解封列表 | `frontend/src/views/HostsPage.vue` |
| 接口 | `POST /api/hosts/:id/check/{ssh\|accounts\|persistence\|exposure\|webshell}`、`GET /api/hosts/:id/firewall`、`POST/DELETE /api/hosts/:id/bans[/:ip]` | `routes_hosts.go` |

尚未做：封禁中心 `Ban{scope}` 与 WAF 共用数据源、sshd 配置自动加固/回滚、批量安装与灰度升级、`host_ports/processes` MCP 工具。Webshell 为特征匹配（非沙箱/静态解析），只用于发现线索，需人工复核。

### 更早遗留（P1+）

- 封禁中心 `Ban{scope}` 重构，WAF 与主机共用数据源。
- 批量安装、灰度升级、`host_ports/processes` 等 MCP 工具。
- 已知限制：Agent 指标采集依赖 `/proc`，Windows 仅用于协议联调，指标为 0。

