package security

import (
	"strings"
	"testing"
	"time"
)

// 站点失血事件必须落到具体域名，不能只报「全站」；取 5xx 次数最多的站点。
func TestBleedingEventNamesWorstSite(t *testing.T) {
	s := &Service{lastEmit: map[string]time.Time{}}
	w := &win{
		total: 100,
		s5:    20,
		sites: map[string]*siteTick{
			"a.example.com": {total: 40, e5: 18},
			"b.example.com": {total: 60, e5: 2},
		},
		paths: map[string]int64{"/": 30},
	}

	s.detect(w, L4Stat{}, time.Now())

	if len(s.events) != 1 {
		t.Fatalf("events = %d, want 1", len(s.events))
	}
	e := s.events[0]
	if e.Kind != "站点失血" {
		t.Fatalf("kind = %q", e.Kind)
	}
	if e.Site != "a.example.com" {
		t.Fatalf("site = %q, want a.example.com", e.Site)
	}
	if !strings.Contains(e.Detail, "该站 18 次（45.0%）") {
		t.Fatalf("detail = %q", e.Detail)
	}
}
