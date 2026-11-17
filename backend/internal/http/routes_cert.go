package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"zjpanel/backend/internal/buildinfo"
	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/mcp"
)

// ===================== 指标 =====================

func (s *Server) registerMetricsRoutes() {
	g := s.r.Group("/api/metrics", s.auth.Require())
	g.GET("/current", func(c *gin.Context) {
		c.JSON(http.StatusOK, s.Metrics.Snapshot())
	})
	g.GET("/history", func(c *gin.Context) {
		c.JSON(http.StatusOK, s.Metrics.History(intParam(c.DefaultQuery("hours", "24"))))
	})
}

// ===================== 请求地图 =====================

func (s *Server) registerLogmapRoutes() {
	g := s.r.Group("/api/logmap", s.auth.Require())
	g.GET("/summary", func(c *gin.Context) {
		minutes := intParam(c.DefaultQuery("minutes", "60"))
		recent := intParam(c.DefaultQuery("recent", "100"))
		withPaths := c.Query("paths") == "1" // 站点请求树用：携带站点内路径明细
		c.JSON(http.StatusOK, s.Logmap.Summary(minutes, recent, withPaths))
	})
	// SSE 实时请求流：每个请求一条 entry 事件（即时推送，无批量聚合），
	// 外加 10s 一帧聚合摘要。Cookie 鉴权对 EventSource 天然可用（同源自动携带）。
	g.GET("/stream", func(c *gin.Context) {
		minutes := intParam(c.DefaultQuery("minutes", "60"))
		w := c.Writer
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // 走反代时禁缓冲
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}
		entries, cancel := s.Logmap.Subscribe()
		defer cancel()
		// 连接建立即推一帧摘要，前端不必等首个 10s 周期
		writeSSE(w, "summary", s.Logmap.Summary(minutes, 100, false))
		flusher.Flush()
		heartbeat := time.NewTicker(15 * time.Second)
		summary := time.NewTicker(10 * time.Second)
		defer heartbeat.Stop()
		defer summary.Stop()
		for {
			select {
			case <-c.Request.Context().Done():
				return
			case e, ok := <-entries:
				if !ok {
					return
				}
				writeSSE(w, "entry", e)
				flusher.Flush()
			case <-summary.C:
				writeSSE(w, "summary", s.Logmap.Summary(minutes, 100, false))
				flusher.Flush()
			case <-heartbeat.C:
				_, _ = io.WriteString(w, ": ping\n\n")
				flusher.Flush()
			}
		}
	})
	g.GET("/geoip", func(c *gin.Context) {
		c.JSON(http.StatusOK, s.Logmap.GeoIPInfo())
	})
	// 服务器位置：GET 当前生效值，POST 手动指定/清除（body: {lat,lon} 设置，{clear:true} 恢复自动）
	g.GET("/serverloc", func(c *gin.Context) {
		c.JSON(http.StatusOK, s.Logmap.ServerLocInfo())
	})
	g.POST("/serverloc", func(c *gin.Context) {
		var req struct {
			Lat   *float64 `json:"lat"`
			Lon   *float64 `json:"lon"`
			Clear bool     `json:"clear"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
			return
		}
		if req.Clear {
			if err := s.Logmap.ClearServerLoc(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			s.audit(c, "logmap.serverloc.clear", "")
			c.JSON(http.StatusOK, gin.H{"ok": true})
			return
		}
		if req.Lat == nil || req.Lon == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 lat/lon"})
			return
		}
		if err := s.Logmap.SetServerLoc(*req.Lon, *req.Lat, ""); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "logmap.serverloc.set", "")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.POST("/geoip/download", func(c *gin.Context) {
		var req struct{ URL string }
		_ = c.ShouldBindJSON(&req)
		if err := s.Logmap.DownloadMMDB(req.URL); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "logmap.geoip.download", "")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.POST("/geoip/upload", func(c *gin.Context) {
		fh, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "缺少文件"})
			return
		}
		f, err := fh.Open()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := s.Logmap.SaveMMDB(data); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "logmap.geoip.upload", fh.Filename)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}

// ===================== 证书 =====================

func (s *Server) registerCertRoutes() {
	g := s.r.Group("/api/cert", s.auth.Require())

	// ACME 账户
	g.GET("/accounts", func(c *gin.Context) {
		var list []model.AcmeAccount
		s.db.Order("id").Find(&list)
		c.JSON(http.StatusOK, list)
	})
	g.POST("/accounts", func(c *gin.Context) {
		var req struct {
			ID      uint   `json:"id"`
			Email   string `json:"email"`
			Type    string `json:"type"`
			DirURL  string `json:"dirUrl"`
			KeyPEM  string `json:"keyPem"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		acct, err := s.Cert.SaveAccount(req.ID, req.Email, req.Type, req.DirURL, req.KeyPEM)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "cert.account.save", acct.Email)
		c.JSON(http.StatusOK, acct)
	})
	g.DELETE("/accounts/:id", func(c *gin.Context) {
		s.db.Delete(&model.AcmeAccount{}, idParam(c))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// DNS 凭证
	g.GET("/dns-providers", func(c *gin.Context) {
		c.JSON(http.StatusOK, s.Cert.DNS.List())
	})
	g.GET("/credentials", func(c *gin.Context) {
		var list []model.DnsCredential
		s.db.Order("id").Find(&list)
		c.JSON(http.StatusOK, list)
	})
	g.POST("/credentials", func(c *gin.Context) {
		var req struct {
			ID      uint            `json:"id"`
			Name    string          `json:"name"`
			Provider string         `json:"provider"`
			Config  map[string]string `json:"config"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		rec, err := s.Cert.SaveCredential(req.ID, req.Name, req.Provider, req.Config)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		s.audit(c, "cert.credential.save", rec.Name)
		c.JSON(http.StatusOK, rec)
	})
	g.DELETE("/credentials/:id", func(c *gin.Context) {
		s.db.Delete(&model.DnsCredential{}, idParam(c))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// 部署目标类型
	g.GET("/targets", func(c *gin.Context) {
		c.JSON(http.StatusOK, s.Cert.Targets.List())
	})

	// 任务
	g.GET("/tasks", func(c *gin.Context) {
		var list []model.CertTask
		s.db.Order("id").Find(&list)
		items := []map[string]any{}
		for _, t := range list {
			m := map[string]any{
				"id": t.ID, "name": t.Name, "domains": t.DomainList(),
				"challenge": t.Challenge, "acmeAccountId": t.AcmeAccountID,
				"dnsCredentialId": t.DnsCredentialID, "renewDays": t.RenewDays,
				"cronSpec": t.CronSpec, "enabled": t.Enabled,
				"deployTargets": json.RawMessage(func() string { if t.DeployTargets == "" { return "[]" }; return t.DeployTargets }()),
				"lastRunId": t.LastRunID, "updatedAt": t.UpdatedAt,
			}
			if info := s.Cert.LatestCert(t.ID); info != nil {
				days := int(timeUntil(info.NotAfter).Hours() / 24)
				m["cert"] = map[string]any{"id": info.ID, "notAfter": info.NotAfter, "daysLeft": days, "status": info.Status}
			}
			if next := s.Cert.NextRunTime(t.ID); !next.IsZero() {
				m["nextRunAt"] = next
			}
			items = append(items, m)
		}
		c.JSON(http.StatusOK, items)
	})
	g.POST("/tasks", func(c *gin.Context) {
		var req struct {
			ID              uint                            `json:"id"`
			Name            string                          `json:"name"`
			Domains         []string                        `json:"domains"`
			Challenge       string                          `json:"challenge"`
			AcmeAccountID   uint                            `json:"acmeAccountId"`
			DnsCredentialID *uint                           `json:"dnsCredentialId"`
			RenewDays       int                             `json:"renewDays"`
			CronSpec        string                          `json:"cronSpec"`
			Enabled         bool                            `json:"enabled"`
			DeployTargets   []map[string]any                `json:"deployTargets"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		domainsJSON, _ := json.Marshal(req.Domains)
		rawTargets, _ := json.Marshal(req.DeployTargets)
		targetsJSON := s.Cert.EncodeTargets(s.Cert.DecodeTargets(string(rawTargets)))
		var task model.CertTask
		if req.ID > 0 {
			if err := s.db.First(&task, req.ID).Error; err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "任务不存在"})
				return
			}
		}
		task.Name = req.Name
		task.Domains = string(domainsJSON)
		task.Challenge = req.Challenge
		task.AcmeAccountID = req.AcmeAccountID
		task.DnsCredentialID = req.DnsCredentialID
		task.RenewDays = req.RenewDays
		task.CronSpec = req.CronSpec
		task.Enabled = req.Enabled
		task.DeployTargets = targetsJSON
		if err := s.db.Save(&task).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		s.Cert.RescheduleTask(&task)
		s.audit(c, "cert.task.save", task.Name)
		c.JSON(http.StatusOK, task)
	})
	g.DELETE("/tasks/:id", func(c *gin.Context) {
		s.db.Delete(&model.CertTask{}, idParam(c))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	g.POST("/tasks/:id/run", func(c *gin.Context) {
		var req struct{ Force bool `json:"force"` }
		_ = c.ShouldBindJSON(&req)
		runID, err := s.Cert.RunTask(idParam(c), req.Force)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "runId": runID})
			return
		}
		s.audit(c, "cert.task.run", c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"ok": true, "runId": runID})
	})

	// 运行历史
	g.GET("/runs", func(c *gin.Context) {
		taskID := intParam(c.Query("taskId"))
		var list []model.CertRun
		q := s.db.Order("id DESC").Limit(100)
		if taskID > 0 {
			q = q.Where("task_id = ?", taskID)
		}
		q.Find(&list)
		c.JSON(http.StatusOK, list)
	})
	g.GET("/runs/:id", func(c *gin.Context) {
		var run model.CertRun
		if err := s.db.First(&run, idParam(c)).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
			return
		}
		c.JSON(http.StatusOK, run)
	})

	// 证书仓库
	g.GET("/infos", func(c *gin.Context) {
		var list []model.CertInfo
		s.db.Order("id DESC").Limit(500).Find(&list)
		c.JSON(http.StatusOK, list)
	})
	g.GET("/infos/:id/file", func(c *gin.Context) {
		kind := c.DefaultQuery("kind", "cert")
		data, err := s.Cert.CertContent(idParam(c), kind)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.Header("Content-Type", "text/plain; charset=utf-8")
		c.String(http.StatusOK, string(data))
	})
}

func intParam(s string) int {
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return n
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// writeSSE 输出一条事件。信封与原 WS 消息一致：{"type":"...","data":...}。
func writeSSE(w io.Writer, typ string, data any) {
	b, _ := json.Marshal(map[string]any{"type": typ, "data": data})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
}

func timeUntil(t time.Time) time.Duration { return time.Until(t) }

var _ = mcp.Tool{}
var _ = buildinfo.Version
