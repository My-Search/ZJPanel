package boot

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

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
		&model.User{}, &model.Setting{}, &model.ApiKey{}, &model.SshHost{},
		&model.Site{}, &model.ComposeProject{},
		&model.AcmeAccount{}, &model.DnsCredential{}, &model.CertTask{}, &model.CertInfo{}, &model.CertRun{},
		&model.PluginRecord{}, &model.PluginSetting{}, &model.MetricSample{}, &model.AuditLog{},
		&model.Notification{}, &model.QuickCmd{},
	); err != nil {
		return nil, err
	}
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

func repairLegacyIDColumn(db *gorm.DB, table, column string) error {
	// 首次启动或旧库尚未建表/加列时无事可做，交给 AutoMigrate 处理
	if !db.Migrator().HasTable(table) || !db.Migrator().HasColumn(table, column) {
		return nil
	}
	var rows []struct {
		ID  uint           `gorm:"column:id"`
		Raw sql.NullString `gorm:"column:raw"`
	}
	if err := db.Raw(fmt.Sprintf("SELECT id, %s AS raw FROM %s", column, table)).Scan(&rows).Error; err != nil {
		return err
	}
	cleared, normalized := 0, 0
	for _, r := range rows {
		if !r.Raw.Valid {
			continue
		}
		s := strings.TrimSpace(r.Raw.String)
		// 空串、负数、非数字等一律视为未关联；ID 0 不是合法项目/站点主键
		n, err := strconv.ParseUint(s, 10, 64)
		if s == "" || err != nil || n == 0 {
			if err := db.Exec(fmt.Sprintf("UPDATE %s SET %s = NULL WHERE id = ?", table, column), r.ID).Error; err != nil {
				return err
			}
			cleared++
			continue
		}
		// 合法但非规范（前后空格、前导零）的文本值改写为整数
		if s != r.Raw.String {
			if err := db.Exec(fmt.Sprintf("UPDATE %s SET %s = ? WHERE id = ?", table, column), n, r.ID).Error; err != nil {
				return err
			}
			normalized++
		}
	}
	if cleared+normalized > 0 {
		slog.Info("历史关联 ID 已清洗", "table", table, "column", column, "cleared", cleared, "normalized", normalized)
	}
	return nil
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
