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
	db, err := gorm.Open(sqlite.Open(cfg.DataDir+"/zjpanel.db"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Setting{}, &model.ApiKey{}, &model.SshHost{},
		&model.Site{}, &model.ComposeProject{},
		&model.AcmeAccount{}, &model.DnsCredential{}, &model.CertTask{}, &model.CertInfo{}, &model.CertRun{},
		&model.PluginRecord{}, &model.PluginSetting{}, &model.MetricSample{}, &model.AuditLog{},
	); err != nil {
		return nil, err
	}
	slog.Info("数据库就绪", "path", cfg.DataDir+"/zjpanel.db")
	return db, nil
}
