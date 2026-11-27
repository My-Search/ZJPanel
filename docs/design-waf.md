# WAF 规则引擎设计（参考 SamWaf）

> 状态：v0.2.x 已落地（`backend/internal/waf` + `/api/waf`；原「WAF 规则」页已并入「WAF」页「策略与规则」标签）
> 参考项目：`SamWaf`（Go 自研 WAF，反向代理式）

## 1. 为什么不是照搬 SamWaf

SamWaf 是**反向代理式 WAF**：自己监听端口、接管全部 HTTP 流量，因此能拿到 method/header/body/cookie/响应体，做规则匹配、挑战页、篡改防护、敏感词过滤。

ZJPanel 的架构契约是**「面板只管理宿主机 nginx，不做流量代理」**（单二进制 + 直接管理宿主 nginx/docker，站点流量始终走 nginx）。把 SamWaf 的引擎原样搬进来只有两条路，都不合适：

| 方案 | 代价 |
|---|---|
| 面板内置一个反代 WAF，站点流量改走面板 | 推翻「站点由 nginx 承载」的产品定位；引入吞吐/稳定性风险；现有 nginx 站点配置全部要改成 upstream 指向面板 |
| 在 nginx 里塞 OpenResty + Lua 规则 | 镜像从 `nginx:alpine` 变成 OpenResty，构建/升级链路全改；Lua 规则引擎可维护性差 |

所以本次增强选择**与现有架构一致的最小闭环**：把「看得见的请求」做深——用 nginx access log 的 URI/查询串/UA/Referer 做内容级检测，命中后复用已有的 nginx geo 封禁通道处置。

**能做到**：SQLi、XSS、命令执行、路径穿越、webshell 访问特征、扫描器 UA、敏感路径探测、自定义正则规则，命中证据落盘可回溯，按 IP 累犯阶梯封禁。

**做不到（必须清楚）**：请求体（POST form/JSON）、Cookie、除 UA/Referer 外的请求头不进入 access log，本层**看不见**。这类攻击只能靠「响应状态异常 + CC/爆破统计」侧写，或后续按路线图补 mirror 方案。

## 2. 从 SamWaf 借鉴的机制

| SamWaf 机制 | 本项目落地方式 |
|---|---|
| `libinjection` 词法级 SQLi/XSS 判定（不依赖正则堆砌） | 直接依赖上游 `github.com/corazawaf/libinjection-go`（与 SamWaf 同版本线），**不复制源码** |
| 多轮 URL 解码（`RawQuery` 已多轮解码）+ HTML 实体/Unicode 还原 | `decodeLayers`（最多 3 层）+ `decodeEntities`（`&lt;`/`&#x27;`/`\u003c`）|
| 多信号判定压误报（libinjection 命中 + 执行信号才判 XSS；SQLi 补高危关键字兜底） | XSS 要求命中 libinjection 且含 `<字母`/`on…=`/`javascript:` 等执行信号；SQLi 命中后要求引号/注释/SQL 结构词等第二信号 |
| 规则动作分级：拦截 / 放行 / 仅记录，按优先级仲裁 | 规则 `action` = block / observe，全局 `blockMode` 可整体降级为观察 |
| 逐条规则可开关、可改动作、自定义规则 | 「WAF」页「策略与规则」标签逐条 Switch + 动作下拉；自定义正则规则（作用范围 url/query/ua/referer）|
| 封禁带时长、累犯递增 | 阶梯封禁：默认 `10 / 60 / 1440` 分钟，超出取最后一级；`banResetHours`（默认 24h）内无新命中则重新从第一级算 |
| 攻击日志保留证据（payload 片段） | 命中记录含解码后的证据片段，环形 1000 条 + 落盘 `data/waf/hits.jsonl`（8MB 滚动）|
| 扫描器 UA 库 | 内置 UA 正则（sqlmap/nikto/nuclei/fscan/xray…），**排除 curl/wget 等通用客户端**避免误杀 |
| 敏感路径 / 后台探测 | 敏感路径内置规则，默认**观察**（误伤面大，先看日志）|

未借鉴的部分及原因：

- **Grule 规则引擎（GRL 表达式）**：表达力强但要引入 hyperjumptech/grule 依赖与规则 DSL，配置面陡增；本项目用「内置类别 + 自定义正则」已覆盖日志层能判定的场景。
- **挑战页 / JS 验证 / Cookie 挑战**：需要接管响应，反代式才有意义；本项目由 nginx 承载，挑战页属于后续 nginx 侧方案。
- **响应体敏感词 / 篡改防护 / 防盗链**：需要代理响应流，不在本层能力范围。
- **IP 组、ASN 封禁**：现有安全中心已有 IP/国家封禁与 GeoIP 展开，IP 组可在封禁面板手动维护 CIDR，暂不单独建模。

## 3. 数据流

```
nginx access_log(zjp 格式)
        │  logmap 增量解析（已有）
        ▼
  logmap.Entry ──SubscribeFast(detail=true)──┬─→ security（RPS/CC/爆破/失血/L4）
                                             └─→ waf（本层）
                                                   │  豁免过滤（127.0.0.1/::1/自定义 CIDR）
                                                   │  规则判定（无锁快照）
                                                   ▼
                                              命中聚合（同 IP+规则 30s 合并计数）
                                                   │
                                    ┌──────────────┴───────────────┐
                                    ▼                              ▼
                         hits.jsonl（证据落盘）          block 命中 → 累犯计数
                                    │                              │
                                    ▼                              ▼
                           /api/waf/hits（页面）        pend 排队（1s 合并）
                                                                   │
                                                                   ▼
                                              security.BanAuto（批量保存策略 + 一次 nginx reload）
                                                                   │
                                                                   ▼
                                                   conf.d/zjp-security.conf（geo + 444）
```

关键点：**封禁必须批量**。攻击期间每条命中触发一次 `nginx -t && reload`，reload 本身就会变成拒绝服务；所以 WAF 侧把封禁请求压进 `pend`，每秒合并一次交给 `security.BanAuto`。

## 4. 判定细节与误报控制

1. **只判参数值，不判参数名（除含引号等异常字符）**：`?style=dark&href=…` 这类键名进 libinjection 会被当成 HTML 属性误报（SamWaf 同款处理）。
2. **SQLi 双信号**：libinjection 词法命中后，仍要求出现 `' " -- /*` 或 `union select … from`、`sleep(`、`information_schema` 等结构词/等号，避免 `1<2 and 3>2` 这类散文比较被误判。
3. **XSS 双信号**：libinjection 命中 + `<字母/!/‌`、`on…=`、`javascript:`、`srcdoc=`、`data:text/html` 之一。
4. **命令执行用强特征**：`${jndi:`、`Runtime.getRuntime().exec`、`ProcessBuilder`、`exec/system/passthru(`、`;|&& cmd` 等，不做「单个单词黑名单」。
5. **敏感路径默认观察**：`/.env`、`/wp-login.php`、`/actuator` 也是正常运维/健康检查的常见路径，先记录让用户确认。
6. **扫描器 UA 不含 curl/wget/python-requests**：通用客户端有大量正常用途。
7. **豁免地址**：默认含本机回环；面板自检、同机运维走 127.0.0.1 不应被判成攻击。
8. **观察模式是默认**：与安全中心既有哲学一致（先跑一天观察），页面切到拦截需要一次显式确认。

## 5. 处置与状态

- `Settings.enabled`：总开关（关闭后不判定、不封禁）。
- `Settings.blockMode`：`observe`（全部规则降级为只记录）/ `block`（按规则动作执行）。默认 `observe`。
- `Settings.autoBan`：block 命中是否真正落封禁；关闭后仅记录（用于「先看这波是不是误判」）。
- `Settings.banLadder`：封禁阶梯（分钟），自动封禁**不允许 0/负数**（永久封禁只保留人工操作入口，避免手滑锁死整片网段），单级上限 7 天。
- `Settings.exempt`：豁免 IP/CIDR。
- 累犯语义：**解封后再次作恶才升级**。封禁期内重复命中只累加计数，不刷新 TTL、不升级阶梯。
- 解封：走安全中心「当前封禁」列表（WAF 封禁与 CC/手动封禁同一份 `policy.json`）。

## 6. 接口

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/waf/view` | 配置（settings + rules）+ 概况统计 |
| PUT | `/api/waf/settings` | 保存设置（校验豁免 CIDR、归一化阶梯） |
| PUT | `/api/waf/rule` | 开关规则 / 切换动作（`{id, enabled, action}`） |
| POST | `/api/waf/rule` | 新增自定义规则（保存前编译正则） |
| DELETE | `/api/waf/rule/:id` | 删除自定义规则（内置规则只能关不能删） |
| GET | `/api/waf/hits` | 命中日志（ip/category/ruleId/site/limit） |
| GET | `/api/waf/stats` | 近 1 小时命中分布 / Top IP / Top 规则 |
| POST | `/api/waf/test` | 规则测试（构造 path/ua → 返回命中，不封禁） |

配置落盘：`data/waf/config.json`（内置规则以代码为准，文件只覆盖开关/动作；代码新增内置规则自动补齐）。

## 7. 后续路线（按收益排序）

1. **请求体覆盖（mirror 方案）**：站点 server 块加 `mirror /zjp-waf-mirror;` + `mirror_request_body`，面板侧异步接收副本做 body 检测。优点：不改流量主链路；缺点：镜像流量有额外带宽/延迟，且「事后判定」只能封 IP 不能阻断当前请求——适合作为「观察增强」，判定到高危 IP 后由 nginx geo 阻断后续请求。
2. **nginx 侧挑战页**：对命中 CC/扫描的 IP 用 `map` 生成 302 到面板挑战页，校验通过后种 cookie 放行（需要 `auth_request` 或 map+cookie 方案，属于 nginx 配置层能力）。
3. **IP 组 / ASN / 威胁情报同步**：把安全中心的国家封禁扩展到 ASN/云厂商段，支持订阅外部恶意 IP 库。
4. **规则模板库**：常见框架（WordPress/Spring/ThinkPHP）的路径+参数模板，一键导入为自定义规则。
5. **WAF 与安全中心事件流合并**：把高危命中并入安全中心 WS 快照，攻击时在同一个时间轴看「统计异常 + 内容命中」。

## 8. 测试

`backend/internal/waf` 单测覆盖：

- 内置规则正/负样本（SQLi 布尔盲注/union、XSS 双重编码、JNDI、穿越、webshell、扫描器 UA、敏感路径、正常搜索/中文参数/curl 客户端/富文本比较）
- 观察模式降级、规则开关、自定义规则命中
- 多轮解码、HTML 实体还原、阶梯封禁取值
- 服务层：命中聚合去重、封禁下发、封禁期内不升级、到期累犯升级、豁免地址、总开关关闭、设置校验
