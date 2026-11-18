package boot

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"

	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/pkg"
)

// migrateCredentialV2 凭据密文一次性升级：legacy 格式（base64(nonce‖ct)，无 AAD）
// → v2 格式（"v2:"+base64(nonce‖ct)，AAD 绑定字段上下文，密文不可跨字段互换）。
// 幂等：已带 v2 前缀或空值跳过；单行失败仅告警跳过，不阻塞启动。
// 读侧 OpenCtx 向后兼容 legacy，迁移失败不影响功能，下次启动自动重试。
func migrateCredentialV2(db *gorm.DB, box *pkg.SecretBox, dataDir string) {
	migrated := 0

	// SSH 主机：password_enc / private_key_enc / passphrase_enc 三列
	type sshCred struct {
		col string
		ctx string
		val *string
	}
	var hosts []struct {
		ID            uint
		PasswordEnc   *string
		PrivateKeyEnc *string
		PassphraseEnc *string
	}
	if err := db.Model(&model.SshHost{}).Select("id", "password_enc", "private_key_enc", "passphrase_enc").
		Find(&hosts).Error; err == nil {
		for _, h := range hosts {
			for _, c := range []sshCred{
				{"password_enc", pkg.CtxSSHPassword, h.PasswordEnc},
				{"private_key_enc", pkg.CtxSSHPrivateKey, h.PrivateKeyEnc},
				{"passphrase_enc", pkg.CtxSSHPassphrase, h.PassphraseEnc},
			} {
				if c.val == nil || *c.val == "" || strings.HasPrefix(*c.val, "v2:") {
					continue
				}
				plain, err := box.OpenCtx(c.ctx, *c.val) // legacy 密文无前缀，OpenCtx 自动按 legacy 解
				if err != nil {
					slog.Warn("凭据 v2 迁移跳过（解密失败）", "table", "ssh_hosts", "id", h.ID, "col", c.col, "err", err)
					continue
				}
				enc, err := box.SealECtx(c.ctx, plain)
				if err != nil {
					slog.Warn("凭据 v2 迁移跳过（加密失败）", "table", "ssh_hosts", "id", h.ID, "col", c.col, "err", err)
					continue
				}
				if err := db.Model(&model.SshHost{}).Where("id = ?", h.ID).Update(c.col, enc).Error; err != nil {
					slog.Warn("凭据 v2 迁移跳过（写回失败）", "table", "ssh_hosts", "id", h.ID, "col", c.col, "err", err)
					continue
				}
				migrated++
			}
		}
	}

	// ACME 账户私钥
	var accts []model.AcmeAccount
	if err := db.Find(&accts).Error; err == nil {
		for _, a := range accts {
			if a.KeyEnc == "" || strings.HasPrefix(a.KeyEnc, "v2:") {
				continue
			}
			if plain, err := box.OpenCtx(pkg.CtxAcmeKey, a.KeyEnc); err == nil {
				if enc, err2 := box.SealECtx(pkg.CtxAcmeKey, plain); err2 == nil {
					if err3 := db.Model(&model.AcmeAccount{}).Where("id = ?", a.ID).Update("key_enc", enc).Error; err3 == nil {
						migrated++
						continue
					}
				}
			}
			slog.Warn("凭据 v2 迁移跳过", "table", "acme_accounts", "id", a.ID)
		}
	}

	// DNS 凭证配置
	var creds []model.DnsCredential
	if err := db.Find(&creds).Error; err == nil {
		for _, r := range creds {
			if r.ConfigEnc == "" || strings.HasPrefix(r.ConfigEnc, "v2:") {
				continue
			}
			if plain, err := box.OpenCtx(pkg.CtxDnsConfig, r.ConfigEnc); err == nil {
				if enc, err2 := box.SealECtx(pkg.CtxDnsConfig, plain); err2 == nil {
					if err3 := db.Model(&model.DnsCredential{}).Where("id = ?", r.ID).Update("config_enc", enc).Error; err3 == nil {
						migrated++
						continue
					}
				}
			}
			slog.Warn("凭据 v2 迁移跳过", "table", "dns_credentials", "id", r.ID)
		}
	}

	// SMTP 应用密码（settings 表 KV 行）
	var st model.Setting
	if err := db.Where("key = ?", "notify.smtp.pass").First(&st).Error; err == nil &&
		st.Value != "" && !strings.HasPrefix(st.Value, "v2:") {
		if plain, err := box.OpenCtx(pkg.CtxSMTPPass, st.Value); err == nil {
			if enc, err2 := box.SealECtx(pkg.CtxSMTPPass, plain); err2 == nil {
				if err3 := db.Model(&model.Setting{}).Where("key = ?", "notify.smtp.pass").Update("value", enc).Error; err3 == nil {
					migrated++
				}
			}
		} else {
			slog.Warn("凭据 v2 迁移跳过", "table", "settings", "key", "notify.smtp.pass")
		}
	}

	// 插件 password 类型设置项（webhooks.smtp_pass / notify-webhook.secret）
	for _, ps := range []struct{ pluginID, key string }{
		{"webhooks", "smtp_pass"},
		{"notify-webhook", "secret"},
	} {
		var rec model.PluginSetting
		if err := db.Where("plugin_id = ? AND key = ?", ps.pluginID, ps.key).First(&rec).Error; err == nil &&
			rec.Value != "" && !strings.HasPrefix(rec.Value, "v2:") {
			if enc, err2 := box.SealECtx(pkg.CtxPluginSetting(ps.pluginID, ps.key), rec.Value); err2 == nil {
				if err3 := db.Model(&model.PluginSetting{}).Where("plugin_id = ? AND key = ?", ps.pluginID, ps.key).Update("value", enc).Error; err3 == nil {
					migrated++
				}
			}
		}
	}

	// mysqladmin 插件连接文件（JSON，Password 为密文）
	migrated += migrateMySQLConns(dataDir, box)

	if migrated > 0 {
		slog.Info("凭据密文升级为 v2（AAD 字段绑定）完成", "rows", migrated)
	}
}

// migrateMySQLConns mysqladmin 插件的 connections.json：Password 字段密文升级。
// 借助 OpenCtx 的双格式兼容直接 seal 覆盖；文件不存在即无数据，静默返回。
func migrateMySQLConns(dataDir string, box *pkg.SecretBox) int {
	p := filepath.Join(dataDir, "plugins-data", "mysqladmin", "connections.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	var conns []map[string]any
	if err := json.Unmarshal(b, &conns); err != nil {
		slog.Warn("凭据 v2 迁移跳过（connections.json 解析失败）", "err", err)
		return 0
	}
	changed := false
	for _, c := range conns {
		pwd, _ := c["password"].(string)
		if pwd == "" || strings.HasPrefix(pwd, "v2:") {
			continue
		}
		plain, err := box.OpenCtx(pkg.CtxMySQLConnPwd, pwd)
		if err != nil {
			slog.Warn("凭据 v2 迁移跳过（解密失败）", "file", "mysqladmin/connections.json", "id", c["id"])
			continue
		}
		c["password"] = box.SealCtx(pkg.CtxMySQLConnPwd, plain)
		changed = true
	}
	if !changed {
		return 0
	}
	out, err := json.MarshalIndent(conns, "", "  ")
	if err != nil {
		return 0
	}
	if err := os.WriteFile(p, out, 0o600); err != nil {
		slog.Warn("凭据 v2 迁移写回失败", "file", "mysqladmin/connections.json", "err", err)
		return 0
	}
	return len(conns)
}
