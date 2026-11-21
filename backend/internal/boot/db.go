package boot

import (
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
