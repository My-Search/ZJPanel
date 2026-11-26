# PRD 增量 v0.2.0：数据库管理插件（dbadmin）

> 取代内置插件 `mysqladmin`（MySQL 单库管理），升级为统一 `dbadmin`：MySQL/MariaDB、PostgreSQL、SQLite、SQL Server、Oracle、Redis、MongoDB。

## 1. 目标

- 一个插件、一套连接管理，覆盖关系型 + 键值 + 文档库；不依赖外部客户端。
- 保持旧 MySQL 插件的库表浏览、结构/DDL、分页数据、行级编辑、SQL 控制台、SQL 文件导入能力。
- 按类型差异化：Redis 提供 db 键空间与命令台；MongoDB 提供库/集合、文档 JSON 编辑与 `RunCommand` 控制台。
- 旧 `mysqladmin/connections.json` 首次启动自动迁移，连接不丢。

## 2. 类型与「库」语义

| 类型 | 驱动 | 库含义 | 表名形态 | 建/删库 |
|---|---|---|---|---|
| MySQL/MariaDB | go-sql-driver/mysql | database | 裸表名 | 支持 |
| PostgreSQL | jackc/pgx/v5 (stdlib) | database | `schema.table` | 支持 |
| SQLite | modernc.org/sqlite（纯 Go） | main/attached | 裸表名 | 不支持 |
| SQL Server | microsoft/go-mssqldb | database | `schema.table` | 支持 |
| Oracle | sijms/go-ora/v2 | owner/schema | `owner.table` 定位 | 不支持 |
| Redis | redis/go-redis/v9 | db0..dbN | 无表（键空间） | 不支持 |
| MongoDB | mongo-driver/v2 | database | collection | 支持（DropDatabase） |

## 3. 能力清单

- 连接：新建/编辑/删除/测试；类型不可编辑时变更；密码 AES-GCM(v2, AAD) 加密落盘，列表不回传。
- 关系型：库/表清单、字段/索引/DDL、分页 + WHERE + 排序、主键定位插入/更新/删除、SQL 控制台（历史 50 条）、SQL 文件导入（DELIMITER / `$tag$` / `GO` / Oracle `/` 拆分）。
- Redis：SCAN 游标分页（批量 TYPE/TTL/MEMORY USAGE）、string/hash/list/set/zset/stream 预览（上限 200，stream 50）、删键、TTL 修改、string 值保存、任意命令台。
- MongoDB：库/集合清单、前 50 篇文档采样字段类型、索引与 createIndex 文本、Extended JSON 过滤/分页、按 `_id` 替换/删除、JSON `RunCommand` 控制台。

## 4. 安全与信任边界

- 连接密码只存密文；接口不回传密文/明文。
- 行级写操作一律参数化（`?` / `$n` / `@pn` / `:n`）；更新/删除要求 key 覆盖完整主键，无主键表只读。
- `/rows` 的 `where`、`order` 仍为面板操作员原始 SQL 片段（沿用旧插件已文档化的信任边界）；`where` 值不参与拼接，仅语句文本。
- SQL 文件导入限制 512MB，逐条执行，默认遇错停止，可勾选继续。

## 5. 验收

| 项 | 验收方式 | 状态 |
|---|---|---|
| SQLite 端到端 | Chinook 数据集拆分建库 → 表/结构/分页/HTTP 响应结构 | 通过（`go test ./plugins/dbadmin`） |
| 方言 SQL 生成 | 单测覆盖分页/计数/引用/占位符 | 通过 |
| SQL 拆分 | MySQL/PG/SQLServer/Oracle 单测 | 通过 |
| 旧连接迁移 | 按 map 读取、补 `type=mysql`、OpenCtx→SealECtx | 代码路径已实现（需真实旧数据手测） |
| 前端 | 单文件、主题联动、桌面/移动抽屉、类型驱动表单、树/标签/网格/键空间/文档编辑 | 已实现，需浏览器手测 |
| MySQL/PG/MSSQL/Oracle/Redis/Mongo 真实实例 | 需外部服务 | 未执行（本机无实例） |

## 6. 已知限制

- Oracle 分页用 12c+ `OFFSET/FETCH`；11g 需另行兼容。
- SQL Server 连接串固定 `encrypt=true&TrustServerCertificate=true`（自签证书可用），老实例如强制明文需后续加开关。
- Redis 单机/哨兵走 URI；Cluster 未适配（SCAN 跨分片需专门实现）。
- SQLite 单连接串行（`SetMaxOpenConns(1)` + busy_timeout），并发写入会排队。
- MongoDB 结构为采样推断，非固定 schema。
- Oracle `DBMS_METADATA.GET_DDL` 受权限限制时会回退到生成 DDL。
