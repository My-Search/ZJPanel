package pkg

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net/smtp"
	"strings"
)

// SendMail 通过 SMTP 发送一封 UTF-8 纯文本邮件（无外部依赖）。
// 端口 465 走隐式 TLS（smtps）；其他端口先尝试 STARTTLS（服务器支持时）。
// user 为空则不做认证（本地中继场景）；非 TLS 连接下 PlainAuth 会被拒绝，
// 因此仅 TLS 已建立或目标是本机时才尝试认证。
// fromAlias 为发件人显示别名（收件箱中发件人位置显示的名字，可中文）；
// fromAddr 为信封发件地址（通常等于 SMTP 用户）。
func SendMail(host string, port int, user, pass, fromAlias, fromAddr, to, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", host, port)
	msg := buildMessage(fromAlias, fromAddr, to, subject, body)

	if port == 465 {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
		if err != nil {
			return fmt.Errorf("TLS 连接失败: %w", err)
		}
		c, err := smtp.NewClient(conn, host)
		if err != nil {
			return fmt.Errorf("SMTP 握手失败: %w", err)
		}
		defer c.Close()
		return sendWithClient(c, host, user, pass, fromAddr, to, msg)
	}

	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("SMTP 连接失败: %w", err)
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return fmt.Errorf("STARTTLS 失败: %w", err)
		}
	}
	return sendWithClient(c, host, user, pass, fromAddr, to, msg)
}

func sendWithClient(c *smtp.Client, host, user, pass, from, to, msg string) error {
	if user != "" {
		_, tlsOK := c.TLSConnectionState()
		if tlsOK || host == "localhost" || host == "127.0.0.1" {
			if ok, _ := c.Extension("AUTH"); ok {
				if err := c.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
					return fmt.Errorf("SMTP 认证失败: %w", err)
				}
			}
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM 失败: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO 失败: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA 失败: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// buildMessage 组装邮件原文：别名/主题按 RFC 2047 Base64 编码（中文安全，ASCII 别名原样），
// 正文整体 Base64，规避 8-bit 传输与行首点号问题。
func buildMessage(fromAlias, fromAddr, to, subject, body string) string {
	sub := base64.StdEncoding.EncodeToString([]byte(subject))
	fromLine := fromAddr
	if fromAlias != "" {
		alias := fromAlias
		if !isAllASCII(alias) {
			alias = "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(alias)) + "?="
		}
		fromLine = alias + " <" + fromAddr + ">"
	}
	return strings.Join([]string{
		"From: " + fromLine,
		"To: " + to,
		"Subject: =?UTF-8?B?" + sub + "?=",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: base64",
		"",
		base64.StdEncoding.EncodeToString([]byte(body)),
	}, "\r\n") + "\r\n"
}

func isAllASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}
