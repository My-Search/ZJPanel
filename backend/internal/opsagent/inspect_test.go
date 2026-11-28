package opsagent

import (
	"strings"
	"testing"
	"time"
)

type fakeSched struct {
	added    []string
	removed  int
	nextByID map[any]time.Time
}

func (f *fakeSched) AddFunc(spec string, fn func()) any {
	f.added = append(f.added, spec)
	id := len(f.added)
	if f.nextByID == nil {
		f.nextByID = map[any]time.Time{}
	}
	f.nextByID[id] = time.Now().Add(1 * time.Hour)
	return id
}
func (f *fakeSched) RemoveByID(id any) { f.removed++ }
func (f *fakeSched) NextByID(id any) time.Time {
	if t, ok := f.nextByID[id]; ok {
		return t
	}
	return time.Time{}
}

func testSvcWithDB(t *testing.T) *Service {
	s := &Service{db: testDB(t), inspectEntries: map[string]any{}}
	return s
}

func TestSaveInspectValidation(t *testing.T) {
	s := testSvcWithDB(t)
	cases := []struct {
		name    string
		task    InspectTask
		wantErr bool
	}{
		{"缺名称", InspectTask{Name: " ", Cron: "0 9 * * *", Items: []string{"security"}, Enabled: true}, true},
		{"坏 cron", InspectTask{Name: "日常巡检", Cron: "bad", Items: []string{"security"}, Enabled: true}, true},
		{"空巡检项", InspectTask{Name: "日常巡检", Cron: "0 9 * * *", Items: nil, Enabled: true}, true},
		{"仅自定义指令", InspectTask{Name: "日常巡检", Cron: "0 9 * * *", Items: nil, CustomPrompt: "检查 nginx 5xx 占比", Enabled: true}, false},
		{"指令过长", InspectTask{Name: "日常巡检", Cron: "0 9 * * *", Items: []string{"security"}, CustomPrompt: strings.Repeat("长", 2001), Enabled: true}, true},
		{"未知巡检项", InspectTask{Name: "日常巡检", Cron: "0 9 * * *", Items: []string{"nope"}, Enabled: true}, true},
		{"坏防御模式", InspectTask{Name: "日常巡检", Cron: "0 9 * * *", Items: []string{"security"}, DefenseMode: "bad", Enabled: true}, true},
		{"合法", InspectTask{Name: "日常巡检", Cron: "0 9 * * *", Items: []string{"security", "certs"}, Enabled: true}, false},
	}
	for _, c := range cases {
		err := s.SaveInspect(c.task)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", c.name, err, c.wantErr)
		}
	}
	list := s.loadInspects()
	if len(list) != 2 || list[0].Name != "日常巡检" {
		t.Fatalf("合法任务未保存: %+v", list)
	}
	if list[0].ID == "" {
		t.Fatal("新建任务未分配 ID")
	}
	if list[0].DefenseMode != ModeObserve {
		t.Fatalf("未指定防御模式的任务应落到默认 observe，实际 %q", list[0].DefenseMode)
	}
	if list[0].CustomPrompt != "检查 nginx 5xx 占比" {
		t.Fatalf("自定义巡检指令未随任务保存: %q", list[0].CustomPrompt)
	}
}

func TestRescheduleAndDelete(t *testing.T) {
	s := testSvcWithDB(t)
	fs := &fakeSched{}
	s.sched = fs
	_ = s.SaveInspect(InspectTask{Name: "a", Cron: "0 9 * * *", Items: []string{"certs"}, Enabled: true})
	_ = s.SaveInspect(InspectTask{Name: "b", Cron: "*/30 * * * *", Items: []string{"security"}, Enabled: true})
	_ = s.SaveInspect(InspectTask{Name: "c", Cron: "0 3 * * 1", Items: []string{"sites"}, Enabled: false})
	// 每次保存都会全量重排（旧 entry 全摘除再注册启用任务），终态：仅启用任务在册
	if len(s.inspectEntries) != 2 {
		t.Fatalf("启用任务应注册 2 个 entry，实际 %d", len(s.inspectEntries))
	}
	views := s.InspectViews()
	if len(views) != 3 {
		t.Fatalf("列表应 3 条（含停用）: %d", len(views))
	}
	for _, v := range views {
		if v.Enabled && v.Next.IsZero() {
			t.Fatalf("启用任务 %s 应有 Next 时间", v.Name)
		}
		if !v.Enabled && !v.Next.IsZero() {
			t.Fatalf("停用任务 %s 不应有 Next 时间", v.Name)
		}
	}
	// 删除一个 → 重排：终态只剩剩余启用任务
	_ = s.DeleteInspect(views[0].ID)
	remainingEnabled := 0
	for _, t := range s.loadInspects() {
		if t.Enabled {
			remainingEnabled++
		}
	}
	if len(s.inspectEntries) != remainingEnabled {
		t.Fatalf("删除后 entry 数应 %d，实际 %d", remainingEnabled, len(s.inspectEntries))
	}
}

func TestRenderInspect(t *testing.T) {
	hostID := uint(3)
	out := renderInspect(InspectTask{Name: "安全巡检", Items: []string{"security", "nope"}, HostID: &hostID})
	if !contains(out, "安全态势") || !contains(out, "security_overview") {
		t.Fatalf("巡检指令缺少项目与工具提示: %s", out)
	}
	if !contains(out, "#3") {
		t.Fatalf("巡检指令缺少绑定主机说明: %s", out)
	}
	if contains(out, "站点可用性") {
		t.Fatal("未勾选的巡检项不应出现")
	}
	if !contains(out, "panel_notify") {
		t.Fatal("巡检指令缺少汇报通知要求")
	}
}

func TestRenderInspectCustom(t *testing.T) {
	out := renderInspect(InspectTask{Name: "自定义巡检", CustomPrompt: "检查 nginx 5xx 占比"})
	if !contains(out, "自定义巡检指令") || !contains(out, "检查 nginx 5xx 占比") {
		t.Fatalf("巡检指令缺少自定义指令段: %s", out)
	}
	if contains(out, "逐项检查") {
		t.Fatal("无固定巡检项时不应出现逐项检查头")
	}
	// 未填自定义指令的任务不应出现该段
	out2 := renderInspect(InspectTask{Name: "常规巡检", Items: []string{"security"}})
	if contains(out2, "自定义巡检指令") || contains(out2, "检查 nginx 5xx 占比") {
		t.Fatalf("未填自定义指令时不应出现该段: %s", out2)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
