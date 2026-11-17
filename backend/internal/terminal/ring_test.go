package terminal

import (
	"bytes"
	"testing"
)

func TestRingBufBasic(t *testing.T) {
	r := NewRingBuf(16)
	r.Write([]byte("hello"))
	if got := string(r.ReadAll()); got != "hello" {
		t.Fatalf("ReadAll = %q, want %q", got, "hello")
	}
	if r.End() != 5 || r.Start() != 0 {
		t.Fatalf("offsets = %d..%d, want 0..5", r.Start(), r.End())
	}
}

func TestRingBufWrap(t *testing.T) {
	r := NewRingBuf(8)
	r.Write([]byte("abcdefgh")) // 恰好填满
	r.Write([]byte("12345"))    // 溢出环绕
	if got := string(r.ReadAll()); got != "fgh12345" {
		t.Fatalf("ReadAll = %q, want %q", got, "fgh12345")
	}
	if r.End() != 13 || r.Start() != 5 {
		t.Fatalf("offsets = %d..%d, want 5..13", r.Start(), r.End())
	}
}

func TestRingBufReadFrom(t *testing.T) {
	r := NewRingBuf(8)
	r.Write([]byte("abcdefgh12345")) // 环内只剩 "fgh12345"（偏移 5..13）

	// 环内偏移：从 9 起读
	got, ok := r.ReadFrom(9)
	if !ok || string(got) != "2345" {
		t.Fatalf("ReadFrom(9) = %q, %v; want %q, true", got, ok, "2345")
	}
	// since == End：空增量
	got, ok = r.ReadFrom(13)
	if !ok || len(got) != 0 {
		t.Fatalf("ReadFrom(13) = %q, %v; want empty, true", got, ok)
	}
	// 已滚出环形缓冲
	if _, ok := r.ReadFrom(4); ok {
		t.Fatal("ReadFrom(4) 应返回 ok=false")
	}
	// 超前偏移
	if _, ok := r.ReadFrom(14); ok {
		t.Fatal("ReadFrom(14) 应返回 ok=false")
	}
	// 增量拼接等于全量
	full := string(r.ReadAll())
	inc1, _ := r.ReadFrom(5)
	inc2, _ := r.ReadFrom(5 + uint64(len(inc1)))
	if string(inc1)+string(inc2) != full {
		t.Fatalf("增量拼接 %q != 全量 %q", string(inc1)+string(inc2), full)
	}
}

func TestRingBufEmpty(t *testing.T) {
	r := NewRingBuf(8)
	if got := r.ReadAll(); len(got) != 0 {
		t.Fatalf("空缓冲 ReadAll = %q", got)
	}
	if r.Start() != 0 || r.End() != 0 {
		t.Fatalf("空缓冲 offsets = %d..%d, want 0..0", r.Start(), r.End())
	}
	got, ok := r.ReadFrom(0)
	if !ok || len(got) != 0 {
		t.Fatalf("ReadFrom(0) = %q, %v; want empty, true", got, ok)
	}
}

func TestRingBufLargeWrite(t *testing.T) {
	r := NewRingBuf(8)
	r.Write(bytes.Repeat([]byte("x"), 100)) // 单次写入超过容量
	if got := string(r.ReadAll()); got != "xxxxxxxx" {
		t.Fatalf("ReadAll = %q, want 8 个 x", got)
	}
	if r.Start() != 92 || r.End() != 100 {
		t.Fatalf("offsets = %d..%d, want 92..100", r.Start(), r.End())
	}
}
