package cleanup

import (
	"errors"
	"testing"
	"time"
)

type fakeCleaner struct {
	name    string
	calls   int
	removed int
	err     error
}

func (f *fakeCleaner) Name() string { return f.name }
func (f *fakeCleaner) Cleanup(time.Time) (int, error) {
	f.calls++
	return f.removed, f.err
}

type fakeScheduler struct {
	specs []string
}

func (f *fakeScheduler) AddFunc(spec string, fn func()) any {
	f.specs = append(f.specs, spec)
	return nil
}

// RunAll 逐个执行全部清理器：单个失败不影响其余。
func TestRunAllContinuesOnError(t *testing.T) {
	m := New()
	ok1 := &fakeCleaner{name: "a", removed: 3}
	bad := &fakeCleaner{name: "b", err: errors.New("boom"), removed: 0}
	ok2 := &fakeCleaner{name: "c", removed: 1}
	m.Register(ok1)
	m.Register(bad)
	m.Register(ok2)

	m.RunAll(time.Now())
	if ok1.calls != 1 || ok2.calls != 1 {
		t.Fatalf("失败清理器不应中断后续: a=%d c=%d", ok1.calls, ok2.calls)
	}
	if bad.calls != 1 {
		t.Fatalf("b 应被调用一次: %d", bad.calls)
	}
}

// Start 挂共享 cron 并启动补跑一次（补跑为异步，不阻塞返回）。
func TestStartRegistersCron(t *testing.T) {
	m := New()
	m.Register(&fakeCleaner{name: "a"})
	sched := &fakeScheduler{}
	m.Start(sched, "30 3 * * *")
	if len(sched.specs) != 1 || sched.specs[0] != "30 3 * * *" {
		t.Fatalf("cron 表达式未注册: %v", sched.specs)
	}
}

// Start 的 sched 为 nil 时仅启动补跑，不 panic。
func TestStartWithoutScheduler(t *testing.T) {
	m := New()
	m.Register(&fakeCleaner{name: "a"})
	m.Start(nil, "")
}
