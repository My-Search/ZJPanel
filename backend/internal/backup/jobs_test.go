package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitJob 轮询等待任务离开 running 态（超时 10s 直接失败）。
func waitJob(t *testing.T, j *BackupJob) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !j.running() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("任务 10s 内未结束")
}

// TestStartJobConcurrentReject 全局互斥：已有任务运行中时新任务必须被拒绝。
func TestStartJobConcurrentReject(t *testing.T) {
	data := t.TempDir()
	s := New(testConfig(data), nil, nil, nil, nil)

	entered := make(chan struct{})
	release := make(chan struct{})
	j1, err := s.startJob(JobCreate, "first", TriggerManual, func(j *BackupJob) error {
		entered <- struct{}{}
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("启动首个任务失败: %v", err)
	}
	<-entered
	if _, err := s.startJob(JobCreate, "second", TriggerManual, func(j *BackupJob) error { return nil }); err == nil {
		t.Fatal("任务执行中再次启动应被拒绝")
	}
	close(release)
	waitJob(t, j1)
	if v := j1.View(0); v.Status != "success" {
		t.Fatalf("首个任务状态 = %s（%s）", v.Status, v.Error)
	}
	// 任务结束后可以再次启动。
	if _, err := s.startJob(JobCreate, "third", TriggerManual, func(j *BackupJob) error { return nil }); err != nil {
		t.Fatalf("任务结束后应可再次启动: %v", err)
	}
}

// TestJobViewSince 快照按全局行号补取：跳过的行不重发，越界回退现存全部。
func TestJobViewSince(t *testing.T) {
	data := t.TempDir()
	s := New(testConfig(data), nil, nil, nil, nil)
	j := &BackupJob{ID: "t", status: "running"}
	j.push(s, "a")
	j.push(s, "b")
	j.push(s, "c")
	if v := j.View(0); len(v.Lines) != 3 || v.Total != 3 || v.Start != 0 {
		t.Fatalf("全量快照异常: %+v", v)
	}
	if v := j.View(1); len(v.Lines) != 2 || v.Lines[0] != "b" || v.Start != 1 {
		t.Fatalf("增量快照异常: %+v", v)
	}
	// since 落在被丢弃区段：回退现存全部行。
	j.push(s, "d")
	for i := 0; i < maxJobLines; i++ {
		j.push(s, "x")
	}
	v := j.View(1)
	if v.Start != j.trimmed || len(v.Lines) != maxJobLines {
		t.Fatalf("丢弃区段回退异常: start=%d total=%d since=1 len=%d", v.Start, v.Total, len(v.Lines))
	}
}

// TestStartRestoreMissingFile 恢复任务：包不存在时任务以 error 终态结束而非 panic。
func TestStartRestoreMissingFile(t *testing.T) {
	data := t.TempDir()
	s := New(testConfig(data), nil, nil, nil, nil)
	job, err := s.StartRestore(filepath.Join(data, "missing.tar.gz"), []string{ItemCerts})
	if err != nil {
		t.Fatalf("启动恢复任务失败: %v", err)
	}
	waitJob(t, job)
	v := job.View(0)
	if v.Status != "error" || v.Error == "" {
		t.Fatalf("缺包恢复应失败: status=%s err=%q", v.Status, v.Error)
	}
}

// TestStartBuildProducesList 任务化创建备份：终态成功、产物可见、历史保留。
func TestStartBuildProducesList(t *testing.T) {
	data := t.TempDir()
	s := New(testConfig(data), nil, nil, nil, nil)
	if err := os.MkdirAll(filepath.Join(data, "certs/1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "certs/1/fullchain.pem"), []byte("cert-body"), 0o644); err != nil {
		t.Fatal(err)
	}
	job, err := s.StartBuild([]string{ItemCerts}, TriggerManual)
	if err != nil {
		t.Fatalf("启动备份任务失败: %v", err)
	}
	waitJob(t, job)
	v := job.View(0)
	if v.Status != "success" {
		t.Fatalf("备份任务失败: %s", v.Error)
	}
	if len(v.Lines) == 0 || !strings.Contains(v.Lines[len(v.Lines)-1], "备份完成") {
		t.Fatalf("任务日志缺少完成摘要: %v", v.Lines)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !strings.HasPrefix(list[0].Name, "zjpanel-backup-") {
		t.Fatalf("备份产物缺失: %+v", list)
	}
	sums := s.JobSummaries()
	if len(sums) != 1 || sums[0].Kind != JobCreate || sums[0].Status != "success" {
		t.Fatalf("历史任务异常: %+v", sums)
	}
}
