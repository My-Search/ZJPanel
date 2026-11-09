package cert

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v5/acme"
	"github.com/go-acme/lego/v5/certcrypto"
	"github.com/go-acme/lego/v5/certificate"
	"github.com/go-acme/lego/v5/challenge"
	"github.com/go-acme/lego/v5/challenge/http01"
	"github.com/go-acme/lego/v5/lego"
	"github.com/go-acme/lego/v5/registration"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"

	"zjpanel/backend/internal/bus"
	"zjpanel/backend/internal/cert/dns"
	"zjpanel/backend/internal/cert/targets"
	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/pkg"
	"zjpanel/backend/internal/ws"
)

// Service certd 式证书管线：申请(ACME)→部署目标→cron 续期 + 运行历史。
// DNS 提供商与部署目标走注册表，插件可扩展。

type Service struct {
	cfg     *pkg.Config
	db      *gorm.DB
	box     *pkg.SecretBox
	bus     *bus.Bus
	hub     *ws.Hub
	DNS     *certdns.DNSRegistry
	Targets *targets.TargetRegistry

	reloadNginx func() (string, error)
	sshDialer   func(hostID uint) (*ssh.Client, error)

	issueMu sync.Mutex // 串行化签发（挑战端口占用）
	cron    CronLike
	cronMu  sync.Mutex
	entries map[uint]any
	stop    chan struct{}
	once    sync.Once
}

// CronLike 避免直接依赖 robfig/cron 的接口（boot 注入实现）。
type CronLike interface {
	AddFunc(spec string, cmd func()) any
	RemoveByID(id any)
	Start()
	Stop()
}

func New(cfg *pkg.Config, db *gorm.DB, box *pkg.SecretBox, b *bus.Bus, hub *ws.Hub, cron CronLike) *Service {
	s := &Service{
		cfg: cfg, db: db, box: box, bus: b, hub: hub,
		DNS: certdns.NewDNSRegistry(), Targets: targets.NewTargetRegistry(),
		cron: cron, entries: map[uint]any{}, stop: make(chan struct{}),
	}
	return s
}

func (s *Service) SetReloadFunc(fn func() (string, error)) { s.reloadNginx = fn }
func (s *Service) SetSSHDialer(fn func(hostID uint) (*ssh.Client, error)) { s.sshDialer = fn }

func (s *Service) ReloadNginx() (string, error) {
	if s.reloadNginx == nil {
		return "", fmt.Errorf("nginx 未就绪")
	}
	return s.reloadNginx()
}

// Start 启动：注册任务 cron + 每日到期巡检；启动时把中断的 run 标记为失败。
func (s *Service) Start() {
	s.db.Model(&model.CertRun{}).Where("status = ?", "running").Updates(map[string]any{"status": "error", "log": gorm.Expr("log || '\n[系统] 面板重启，任务中断'")})
	var tasks []model.CertTask
	s.db.Where("enabled = ?", true).Find(&tasks)
	for _, t := range tasks {
		s.scheduleTask(&t)
	}
	s.cron.AddFunc("30 6 * * *", s.dailyMonitor)
	s.cron.Start()
	slog.Info("证书服务就绪", "任务数", len(tasks))
}

func (s *Service) Close() {
	s.once.Do(func() { close(s.stop) })
	s.cron.Stop()
}

func (s *Service) scheduleTask(t *model.CertTask) {
	s.cronMu.Lock()
	defer s.cronMu.Unlock()
	if old, ok := s.entries[t.ID]; ok {
		s.cron.RemoveByID(old)
		delete(s.entries, t.ID)
	}
	if !t.Enabled || t.ID == 0 {
		return
	}
	spec := t.CronSpec
	if spec == "" {
		spec = "0 3 * * *"
	}
	id := t.ID
	cronSpec := spec
	entry := s.cron.AddFunc(cronSpec, func() {
		if _, err := s.RunTask(id, false); err != nil {
			slog.Error("证书任务执行失败", "task", id, "err", err)
		}
	})
	s.entries[t.ID] = entry
}

// RescheduleTask 任务变更后重排 cron。
func (s *Service) RescheduleTask(t *model.CertTask) { s.scheduleTask(t) }

// NextRunTime 任务下次执行时间（无 cron 或未启用返回零值）。
func (s *Service) NextRunTime(taskID uint) time.Time {
	s.cronMu.Lock()
	id, ok := s.entries[taskID]
	s.cronMu.Unlock()
	if !ok {
		return time.Time{}
	}
	type nexter interface {
		NextByID(any) time.Time
	}
	if n, ok2 := s.cron.(nexter); ok2 {
		return n.NextByID(id)
	}
	return time.Time{}
}

// dailyMonitor 每日巡检：临近到期自动续期 + 发布 cert.expiring 事件。
func (s *Service) dailyMonitor() {
	var tasks []model.CertTask
	s.db.Where("enabled = ?", true).Find(&tasks)
	for _, t := range tasks {
		info := s.LatestCert(t.ID)
		days := 3650
		if info != nil {
			days = int(time.Until(info.NotAfter).Hours() / 24)
		}
		if info == nil || days <= t.RenewDaysOrDefault() {
			if _, err := s.RunTask(t.ID, false); err != nil {
				slog.Error("自动续期失败", "task", t.ID, "err", err)
				s.bus.Publish("cert.failed", map[string]any{"taskId": t.ID, "error": err.Error()})
			}
		} else if days <= 30 {
			s.bus.Publish("cert.expiring", map[string]any{
				"taskId": t.ID, "domains": t.DomainList(), "daysLeft": days,
			})
		}
	}
}

// ---- ACME 账户 ----

type acmeUser struct {
	email string
	reg   *acme.ExtendedAccount
	key   crypto.Signer
}

func (u *acmeUser) GetEmail() string                        { return u.email }
func (u *acmeUser) GetRegistration() *acme.ExtendedAccount { return u.reg }
func (u *acmeUser) GetPrivateKey() crypto.Signer        { return u.key }

// SaveAccount 新建/更新 ACME 账户（无 key 时生成 ECDSA P-256）。
func (s *Service) SaveAccount(id uint, email, typ, dirURL, keyPEM string) (*model.AcmeAccount, error) {
	if email == "" {
		return nil, fmt.Errorf("邮箱必填")
	}
	if keyPEM == "" {
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalECPrivateKey(priv)
		if err != nil {
			return nil, err
		}
		keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	}
	if _, err := parseAccountKey(keyPEM); err != nil {
		return nil, fmt.Errorf("账户私钥无效: %w", err)
	}
	enc, err := s.box.SealE(keyPEM)
	if err != nil {
		return nil, err
	}
	var acct model.AcmeAccount
	if id > 0 {
		if err := s.db.First(&acct, id).Error; err != nil {
			return nil, fmt.Errorf("账户不存在")
		}
	} else {
		acct = model.AcmeAccount{}
	}
	acct.Email, acct.Type, acct.DirURL, acct.KeyEnc = email, typ, dirURL, enc
	if err := s.db.Save(&acct).Error; err != nil {
		return nil, err
	}
	return &acct, nil
}

func parseAccountKey(pemStr string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("PEM 解析失败")
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	return certcrypto.ParsePEMPrivateKey([]byte(pemStr))
}

func (s *Service) loadAccount(id uint) (*acmeUser, *model.AcmeAccount, error) {
	var acct model.AcmeAccount
	if err := s.db.First(&acct, id).Error; err != nil {
		return nil, nil, fmt.Errorf("ACME 账户不存在")
	}
	keyPEM, err := s.box.Open(acct.KeyEnc)
	if err != nil {
		return nil, nil, fmt.Errorf("账户私钥解密失败: %w", err)
	}
	key, err := parseAccountKey(keyPEM)
	if err != nil {
		return nil, nil, err
	}
	return &acmeUser{email: acct.Email, key: key}, &acct, nil
}

func dirURLOf(acct *model.AcmeAccount) string {
	switch acct.Type {
	case "staging":
		return "https://acme-staging-v02.api.letsencrypt.org/directory"
	case "custom":
		if acct.DirURL != "" {
			return acct.DirURL
		}
	}
	return "https://acme-v02.api.letsencrypt.org/directory"
}

// ---- DNS 凭证 ----

func (s *Service) SaveCredential(id uint, name, provider string, cfgMap map[string]string) (*model.DnsCredential, error) {
	def, err := s.DNS.Get(provider)
	if err != nil {
		return nil, err
	}
	for _, f := range def.Fields {
		if f.Required && strings.TrimSpace(cfgMap[f.Key]) == "" {
			return nil, fmt.Errorf("%s 必填", f.Title)
		}
	}
	b, _ := json.Marshal(cfgMap)
	enc, err := s.box.SealE(string(b))
	if err != nil {
		return nil, err
	}
	var rec model.DnsCredential
	if id > 0 {
		if err := s.db.First(&rec, id).Error; err != nil {
			return nil, fmt.Errorf("凭证不存在")
		}
	}
	rec.Name, rec.Provider, rec.ConfigEnc = name, provider, enc
	if err := s.db.Save(&rec).Error; err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *Service) loadCredential(id uint) (map[string]string, *model.DnsCredential, error) {
	var rec model.DnsCredential
	if err := s.db.First(&rec, id).Error; err != nil {
		return nil, nil, fmt.Errorf("DNS 凭证不存在")
	}
	raw, err := s.box.Open(rec.ConfigEnc)
	if err != nil {
		return nil, nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, nil, err
	}
	return m, &rec, nil
}

// ---- 任务 ----

func (s *Service) LatestCert(taskID uint) *model.CertInfo {
	var info model.CertInfo
	err := s.db.Where("task_id = ? AND status = ?", taskID, "active").Order("id DESC").First(&info).Error
	if err != nil {
		return nil
	}
	return &info
}

// RunTask 管线执行：skip 检查 → ACME 申请 → 保存 → 部署目标（并行）。
func (s *Service) RunTask(taskID uint, force bool) (uint, error) {
	s.issueMu.Lock()
	defer s.issueMu.Unlock()

	var task model.CertTask
	if err := s.db.First(&task, taskID).Error; err != nil {
		return 0, fmt.Errorf("任务不存在")
	}
	run := &model.CertRun{TaskID: taskID, Status: "running", StartedAt: time.Now(), Log: ""}
	if err := s.db.Create(run).Error; err != nil {
		return 0, err
	}
	s.db.Model(&task).Update("last_run_id", run.ID)
	lg := &runLogger{svc: s, run: run, mu: &sync.Mutex{}}
	defer lg.flush()

	finish := func(status string, err error) (uint, error) {
		now := time.Now()
		run.Status = status
		run.FinishedAt = &now
		// 最终状态与结束时间落库（flush 只写日志字段）
		if err := s.db.Model(&model.CertRun{}).Where("id = ?", run.ID).
			Updates(map[string]any{"status": status, "finished_at": now}).Error; err != nil {
			slog.Error("更新运行状态失败", "runId", run.ID, "err", err)
		}
		lg.flush()
		if err != nil {
			lg.line("ERROR", err.Error())
			lg.flush()
		}
		return run.ID, err
	}

	lg.line("INFO", fmt.Sprintf("任务 %q 开始（域名: %s, 挑战: %s）", task.Name, strings.Join(task.DomainList(), ", "), task.Challenge))

	// skip 检查（certd SkipWhenSucceed 思路：未临近到期直接复用上次结果）
	if info := s.LatestCert(taskID); info != nil && !force {
		days := time.Until(info.NotAfter)
		if days > time.Duration(task.RenewDaysOrDefault())*24*time.Hour {
			lg.line("INFO", fmt.Sprintf("证书将在 %d 天后到期（%s），未到续期窗口 %d 天，跳过", int(days.Hours()/24), info.NotAfter.Format("2006-01-02"), task.RenewDaysOrDefault()))
			return finish("skip", nil)
		}
	}

	// 1. ACME 客户端
	user, acct, err := s.loadAccount(task.AcmeAccountID)
	if err != nil {
		return finish("error", err)
	}
	cfgLego := lego.NewConfig(user)
	cfgLego.CADirURL = dirURLOf(acct)
	client, err := lego.NewClient(cfgLego)
	if err != nil {
		return finish("error", err)
	}
	lg.line("INFO", fmt.Sprintf("ACME 账户 %s（%s）→ %s", acct.Email, acct.Type, cfgLego.CADirURL))

	// 2. 挑战
	switch task.Challenge {
	case "http":
		if err := client.Challenge.SetHTTP01Provider(http01.NewProviderServer("", fmt.Sprintf("%d", s.cfg.ChallengePort))); err != nil {
			return finish("error", err)
		}
		lg.line("INFO", fmt.Sprintf("HTTP-01：挑战端口 %d（nginx 已将 /.well-known/acme-challenge 转发至此）", s.cfg.ChallengePort))
	case "dns":
		provider, err := s.buildDNSProvider(&task, lg)
		if err != nil {
			return finish("error", err)
		}
		if err := client.Challenge.SetDNS01Provider(provider); err != nil {
			return finish("error", err)
		}
		lg.line("INFO", "DNS-01：TXT 记录验证（含传播检查）")
	default:
		return finish("error", fmt.Errorf("未知挑战类型: %s", task.Challenge))
	}

	// 3. 注册/复用账户
	if acct.Reg == "" && user.reg == nil {
		reg, err := client.Registration.Register(context.Background(), registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			if strings.Contains(err.Error(), "account already exists") || strings.Contains(err.Error(), "already registered") {
				lg.line("INFO", "账户已存在，复用密钥")
			} else {
				return finish("error", fmt.Errorf("账户注册失败: %w", err))
			}
		} else {
			user.reg = reg
			s.db.Model(acct).Update("reg", reg.Location)
			lg.line("INFO", "ACME 账户注册成功")
		}
	}

	// 4. 申请证书
	lg.line("INFO", "开始申请证书……")
	res, err := client.Certificate.Obtain(context.Background(), certificate.ObtainRequest{
		Domains: task.DomainList(),
		Bundle:  true,
	})
	if err != nil {
		return finish("error", fmt.Errorf("证书申请失败: %w", err))
	}
	leaf, err := parseLeaf(res.Certificate)
	if err != nil {
		return finish("error", err)
	}
	lg.line("INFO", fmt.Sprintf("申请成功：%s（有效期至 %s）", leaf.Subject.CommonName, leaf.NotAfter.Format("2006-01-02")))

	// 5. 落盘 + 证书仓库
	dir := fmt.Sprintf("%s/certs/%d/%d", s.cfg.DataDir, taskID, run.ID)
	if err := pkgEnsureDir(dir); err != nil {
		return finish("error", err)
	}
	certPath, keyPath := dir+"/fullchain.pem", dir+"/privkey.pem"
	if err := osWriteFile(certPath, res.Certificate); err != nil {
		return finish("error", err)
	}
	if err := osWriteFile(keyPath, res.PrivateKey); err != nil {
		return finish("error", err)
	}
	domainsJSON, _ := json.Marshal(task.DomainList())
	s.db.Model(&model.CertInfo{}).Where("task_id = ? AND status = ?", taskID, "active").Update("status", "inactive")
	info := &model.CertInfo{
		TaskID: &taskID, Domains: string(domainsJSON),
		NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter, Status: "active",
		CertPath: certPath, KeyPath: keyPath, IssuedAt: time.Now(),
	}
	if err := s.db.Create(info).Error; err != nil {
		return finish("error", err)
	}
	run.CertInfoID = &info.ID

	// 6. 部署目标（并行，独立节点日志）
	var deploys []DeployTargetConfig
	if err := json.Unmarshal([]byte(task.DeployTargets), &deploys); err == nil && len(deploys) > 0 {
		lg.line("INFO", fmt.Sprintf("执行 %d 个部署目标……", len(deploys)))
		var wg sync.WaitGroup
		var deployErrs sync.Map
		for i, d := range deploys {
			wg.Add(1)
			go func(idx int, dt DeployTargetConfig) {
				defer wg.Done()
				node := fmt.Sprintf("deploy[%d]%s", idx, dt.Type)
				def, err := s.Targets.Get(dt.Type)
				if err != nil {
					lg.line("ERROR", node+": "+err.Error())
					deployErrs.Store(idx, err)
					return
				}
				out, err := def.Runner(targets.DeployArgs{
					TaskID: taskID, Domains: task.DomainList(),
					CertPEM: res.Certificate, KeyPEM: res.PrivateKey,
					NotAfter: leaf.NotAfter, Config: dt.Config,
				})
				if err != nil {
					lg.line("ERROR", node+": "+err.Error())
					deployErrs.Store(idx, err)
				} else {
					lg.line("INFO", node+": "+out)
				}
			}(i, d)
		}
		wg.Wait()
		deployErrCount := 0
		deployErrs.Range(func(_, _ any) bool { deployErrCount++; return true })
		if deployErrCount > 0 {
			return finish("error", fmt.Errorf("证书已签发，但 %d 个部署目标失败", deployErrCount))
		}
	} else {
		lg.line("INFO", "未配置部署目标，证书仅保存到仓库")
	}

	s.bus.Publish("cert.issued", map[string]any{
		"taskId": taskID, "domains": task.DomainList(), "notAfter": leaf.NotAfter.Format("2006-01-02"),
	})
	return finish("success", nil)
}

func (s *Service) buildDNSProvider(task *model.CertTask, lg *runLogger) (challenge.Provider, error) {
	if task.DnsCredentialID == nil {
		return nil, fmt.Errorf("DNS 挑战需要选择凭证")
	}
	cfgMap, cred, err := s.loadCredential(*task.DnsCredentialID)
	if err != nil {
		return nil, err
	}
	def, err := s.DNS.Get(cred.Provider)
	if err != nil {
		return nil, err
	}
	lg.line("INFO", fmt.Sprintf("DNS 提供商: %s（%s）", def.Title, def.Name))
	return def.Factory(cfgMap)
}

// ---- 证书仓库 ----

func (s *Service) CertContent(infoID uint, kind string) ([]byte, error) {
	var info model.CertInfo
	if err := s.db.First(&info, infoID).Error; err != nil {
		return nil, fmt.Errorf("证书不存在")
	}
	path := info.CertPath
	if kind == "key" {
		path = info.KeyPath
	}
	return osReadFile(path)
}

var _ = challenge.Provider(nil)
var _ = ecdsa.PrivateKey{}
