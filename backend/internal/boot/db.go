package boot

import (
	"fmt"
	"log/slog"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/pkg"
)

func OpenDB(cfg *pkg.Config) (*gorm.DB, error) {
	// WAL + busy_timeout：写不再阻塞读、并发读写时等锁而非报 database is locked；
	// synchronous(NORMAL) 是 WAL 推荐档（掉电最多丢最后一个事务，不损坏库）。
	// PrepareStmt 缓存预编译语句，省去每次查询的重复编译。
	dsn := cfg.DataDir + "/zjpanel.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:      gormlogger.Default.LogMode(gormlogger.Silent),
		PrepareStmt: true,
	})
	if err != nil {
		return nil, err
	}
	// SQLite 单写多读：池限 4 足够面板自身负载，避免突发请求堆出大量连接各持一份页缓存
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(4)
		sqlDB.SetMaxIdleConns(4)
	}
	// 先清洗历史库脏值，再 AutoMigrate：旧版本曾把可空 ID 关联字段以字符串写入
	// （空串/非数字），SQLite 的 integer 列允许存文本，扫描回 *uint 时会报
	// `converting driver.Value type string ("") to a uint: invalid syntax`，
	// 导致 /api/sites 等读取接口 500。
	repairLegacyComposeIDs(db)
	if err := db.AutoMigrate(
		&model.User{}, &model.Setting{}, &model.ApiKey{}, &model.SshHost{}, &model.AgentCommand{}, &model.HostCheck{},
		&model.Site{}, &model.ComposeProject{},
		&model.AcmeAccount{}, &model.DnsCredential{}, &model.CertTask{}, &model.CertInfo{}, &model.CertRun{},
		&model.PluginRecord{}, &model.PluginSetting{}, &model.MetricSample{}, &model.AuditLog{},
		&model.Notification{}, &model.QuickCmd{},
		&model.OpsAgentSession{}, &model.OpsAgentMessage{}, &model.OpsAgentMemory{}, &model.OpsAgentKnowledge{},
	); err != nil {
		return nil, err
	}
	// AutoMigrate 可能把历史 TEXT 列重建为 integer（数据原样搬运），再兜底清洗一次；
	// 同时覆盖旧库该列 NOT NULL 时前置清洗失败、迁移放宽约束后的情况。
	repairLegacyComposeIDs(db)
	// 旧库没有 local 列时，AutoMigrate 加列后历史主机该值为 NULL；
	// 而 /api/ssh/hosts 用 `local = false` 过滤，会把老主机全部漏掉（主机页却能看到）。
	migrateSSHHostLocal(db)
	seedQuickCmds(db)
	migrateQuickCmdColors(db)
	slog.Info("数据库就绪", "path", cfg.DataDir+"/zjpanel.db")
	return db, nil
}

// repairLegacyComposeIDs 清洗 compose 关联字段的历史脏值。
// 旧版本曾把 sites.compose_project_id / compose_projects.site_id 当字符串写
// （未关联时为 ""），SQLite 不会因列亲和性拒绝文本，这些值在同一列里与
// integer 混存，database/sql 扫描 *uint 时空串/非数字串会直接报
// invalid syntax，使整张表的读取接口失败。这里把无法解析为 ID 的值归一为
// NULL，并把带空格/前导零的合法数字写成标准整数；有效关联原样保留。
func repairLegacyComposeIDs(db *gorm.DB) {
	targets := []struct{ table, column string }{
		{"sites", "compose_project_id"},
		{"compose_projects", "site_id"},
	}
	for _, t := range targets {
		if err := repairLegacyIDColumn(db, t.table, t.column); err != nil {
			slog.Warn("历史关联 ID 清洗失败", "table", t.table, "column", t.column, "err", err)
		}
	}
}

// repairLegacyIDColumn 用纯 SQL 清洗，避免「为了清洗坏数据先扫描坏数据」：
// v1.0.9 的 Go 侧逐行 Scan 在极端脏值（BLOB/超长数字等）下仍可能中断。
func repairLegacyIDColumn(db *gorm.DB, table, column string) error {
	// 首次启动或旧库尚未建表/加列时无事可做，交给 AutoMigrate 处理
	if !db.Migrator().HasTable(table) || !db.Migrator().HasColumn(table, column) {
		return nil
	}
	col := fmt.Sprintf("`%s`", column)
	tbl := fmt.Sprintf("`%s`", table)

	// 1) 空串、负数、非数字、0、超长数字 → NULL（非法关联宁缺毋滥）
	clearSQL := fmt.Sprintf(
		"UPDATE %s SET %s = NULL WHERE %s IS NOT NULL AND ("+
			"CAST(CAST(%s AS TEXT) AS INTEGER) <= 0 OR (typeof(%s) <> 'integer' AND ("+
			"TRIM(CAST(%s AS TEXT)) = '' OR TRIM(CAST(%s AS TEXT)) GLOB '*[^0-9]*' OR "+
			"LENGTH(TRIM(CAST(%s AS TEXT))) > 19))"+
			")",
		tbl, col, col, col, col, col, col, col,
	)
	res := db.Exec(clearSQL)
	if res.Error != nil {
		return res.Error
	}
	// 2) 合法但非规范（前后空格、前导零）的文本值改写为标准整数
	normSQL := fmt.Sprintf(
		"UPDATE %s SET %s = CAST(TRIM(CAST(%s AS TEXT)) AS INTEGER) WHERE %s IS NOT NULL AND typeof(%s) = 'text' AND "+
			"TRIM(CAST(%s AS TEXT)) <> '' AND TRIM(CAST(%s AS TEXT)) NOT GLOB '*[^0-9]*' AND "+
			"CAST(CAST(TRIM(CAST(%s AS TEXT)) AS INTEGER) AS TEXT) <> CAST(%s AS TEXT)",
		tbl, col, col, col, col, col, col, col, col,
	)
	norm := db.Exec(normSQL)
	if norm.Error != nil {
		return norm.Error
	}
	if res.RowsAffected+norm.RowsAffected > 0 {
		slog.Info("历史关联 ID 已清洗", "table", table, "column", column,
			"cleared", res.RowsAffected, "normalized", norm.RowsAffected)
	}
	return nil
}

// migrateSSHHostLocal 把历史主机的 local 归一为 0（非内置本机）。
// 旧版本还没有 local 列，SQLite 加列后旧行是 NULL；而 SSH 清单按 local=false 过滤，
// 这些主机在「主机」页可见、在证书部署/终端等 SSH 下拉里却消失。
func migrateSSHHostLocal(db *gorm.DB) {
	if !db.Migrator().HasTable("ssh_hosts") || !db.Migrator().HasColumn("ssh_hosts", "local") {
		return
	}
	res := db.Exec("UPDATE ssh_hosts SET local = 0 WHERE local IS NULL")
	if res.Error != nil {
		slog.Warn("历史主机 local 字段修复失败", "err", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		slog.Info("历史主机 local 字段已修复", "rows", res.RowsAffected)
	}
}

// seedQuickCmds 首次启动预置一组常用命令（终端页一键执行）。
func seedQuickCmds(db *gorm.DB) {
	var cnt int64
	db.Model(&model.QuickCmd{}).Count(&cnt)
	if cnt > 0 {
		return
	}
	seeds := []model.QuickCmd{
		{Name: "磁盘占用", Command: "df -h", Color: "#1677ff"},
		{Name: "内存", Command: "free -h", Color: "#f5222d"},
		{Name: "负载", Command: "uptime", Color: "#fa8c16"},
		{Name: "端口监听", Command: "ss -tlnp", Color: "#13c2c2"},
		{Name: "Docker 容器", Command: "docker ps -a", Color: "#52c41a"},
		{Name: "系统日志", Command: "journalctl -n 50 --no-pager", Color: "#722ed1"},
		{Name: "系统信息", Command: "uname -a && cat /etc/os-release | head -2", Color: "#eb2f96"},
		{Name: "最近登录", Command: "last -20", Color: "#faad14"},
	}
	if err := db.Create(&seeds).Error; err != nil {
		slog.Warn("常用命令种子写入失败", "err", err)
	}
}

// migrateQuickCmdColors 旧版种子是浅粉彩底色（深色主题下白字配浅底几乎看不清），
// 一次性换成同色相的饱和色；用户后来自己挑的颜色不受影响。
func migrateQuickCmdColors(db *gorm.DB) {
	legacy := map[string]string{
		"#e6f4ff": "#1677ff", "#fff1f0": "#f5222d", "#f9f0ff": "#722ed1",
		"#e6fffb": "#13c2c2", "#fffbe6": "#faad14", "#fff2e8": "#fa8c16",
		"#fcffe6": "#52c41a", "#f0f5ff": "#2f54eb",
	}
	for from, to := range legacy {
		db.Exec(`UPDATE quick_cmds SET color = ? WHERE LOWER(color) = ?`, to, from)
	}
}
