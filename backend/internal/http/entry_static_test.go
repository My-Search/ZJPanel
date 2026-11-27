package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"zjpanel/backend/internal/model"
)

func newEntryTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Setting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	s := &Server{db: db, r: r}
	s.RegisterStatic(r)
	return s
}

func doRequest(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	s.r.ServeHTTP(w, req)
	return w
}

func TestStaticEntryGate(t *testing.T) {
	s := newEntryTestServer(t)

	// 未启用入口：根路径返回 SPA index.html。
	w := doRequest(t, s, "/")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<div id="app">`) {
		t.Fatalf("未启用入口时 / 应返回 index.html, code=%d", w.Code)
	}

	// 启用入口 /secret-x。
	if err := s.db.Save(&model.Setting{Key: entryPathSettingKey, Value: "/secret-x"}).Error; err != nil {
		t.Fatal(err)
	}

	// 根与未知路径 404，且不返回 index.html。
	for _, p := range []string{"/", "/login", "/secret-xxyz", "/dashboard"} {
		w := doRequest(t, s, p)
		if w.Code != http.StatusNotFound {
			t.Errorf("入口启用后 %s 应 404, got %d", p, w.Code)
		}
		if strings.Contains(w.Body.String(), `<div id="app">`) {
			t.Errorf("入口启用后 %s 不应返回 SPA", p)
		}
	}

	// 入口本身及其子路径返回 index.html 并注入 base。
	for _, p := range []string{"/secret-x", "/secret-x/", "/secret-x/settings", "/secret-x/login"} {
		w := doRequest(t, s, p)
		if w.Code != http.StatusOK {
			t.Errorf("入口路径 %s 应 200, got %d", p, w.Code)
			continue
		}
		if !strings.Contains(w.Body.String(), `window.__ZJPANEL_BASE__="/secret-x"`) {
			t.Errorf("入口路径 %s 未注入 base", p)
		}
	}
}
