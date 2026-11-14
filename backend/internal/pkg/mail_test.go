package pkg

import (
	"bufio"
	"encoding/base64"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

// fakeSMTPSink 最小 SMTP 服务端：应答 EHLO/MAIL/RCPT/DATA/QUIT，收集 DATA 原文。
func fakeSMTPSink(t *testing.T, ln net.Listener, expectAuth bool) (body func() string, done chan struct{}) {
	t.Helper()
	bodyCh := make(chan string, 1)
	done = make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := textproto.NewReader(bufio.NewReader(conn))
		w := textproto.NewWriter(bufio.NewWriter(conn))
		pw := func(code int, s string) { w.PrintfLine("%d %s", code, s) }
		pw(220, "test ESMTP")
		for {
			line, err := r.ReadLine()
			if err != nil {
				return
			}
			upper := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(upper, "EHLO"):
				// 多行应答：中间行 250-xxx，末行 250 xxx（不得带连字符）
				w.PrintfLine("250-test")
				if expectAuth {
					w.PrintfLine("250 AUTH PLAIN")
				} else {
					pw(250, "OK")
				}
			case strings.HasPrefix(upper, "AUTH"):
				pw(235, "ok")
			case strings.HasPrefix(upper, "MAIL FROM"):
				pw(250, "ok")
			case strings.HasPrefix(upper, "RCPT TO"):
				pw(250, "ok")
			case strings.HasPrefix(upper, "DATA"):
				pw(354, "go")
				var sb strings.Builder
				for {
					l, err := r.ReadLine()
					if err != nil {
						return
					}
					if l == "." {
						break
					}
					sb.WriteString(l + "\r\n")
				}
				bodyCh <- sb.String()
				pw(250, "ok")
			case strings.HasPrefix(upper, "QUIT"):
				pw(221, "bye")
				return
			}
		}
	}()
	return func() string { return <-bodyCh }, done
}

func TestSendMailPlain(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	getBody, done := fakeSMTPSink(t, ln, false)

	err = SendMail("127.0.0.1", ln.Addr().(*net.TCPAddr).Port, "", "",
		"panel@zj.local", "admin@example.com", "测试主题", "通知正文第一行\n第二行")
	if err != nil {
		t.Fatalf("SendMail: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SMTP 会话超时")
	}
	raw := getBody()
	if !strings.Contains(raw, "Subject: =?UTF-8?B?") {
		t.Errorf("主题未按 RFC 2047 编码:\n%s", raw)
	}
	if !strings.Contains(raw, "To: admin@example.com") {
		t.Errorf("缺少 To 头:\n%s", raw)
	}
	// 正文整体 base64，可解回原文
	for _, line := range strings.Split(raw, "\r\n") {
		if strings.HasPrefix(line, "=?") || line == "" || strings.Contains(line, ": ") {
			continue
		}
		if dec, derr := base64.StdEncoding.DecodeString(line); derr == nil {
			if strings.Contains(string(dec), "第二行") {
				return // 找到正文即通过
			}
		}
	}
	t.Errorf("正文 base64 解码后未包含原文:\n%s", raw)
}

func TestSendMailWithAuth(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	getBody, done := fakeSMTPSink(t, ln, true)

	err = SendMail("localhost", ln.Addr().(*net.TCPAddr).Port, "panel", "secret",
		"panel@zj.local", "admin@example.com", "hello", "body")
	if err != nil {
		t.Fatalf("SendMail: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SMTP 会话超时")
	}
	if b := getBody(); !strings.Contains(b, "Subject: =?UTF-8?B?aGVsbG8=?=") {
		t.Errorf("主题编码异常:\n%s", b)
	}
}
