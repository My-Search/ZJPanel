package terminal

import (
	"fmt"
	"log/slog"
	"sync"

	"gorm.io/gorm"
	"golang.org/x/crypto/ssh"

	"zjpanel/backend/internal/model"
	"zjpanel/backend/internal/pkg"
)

// DialHostByID 供证书部署目标等模块复用：按主机 ID 建立SSH 连接。
func DialHostByID(db *gorm.DB, box *pkg.SecretBox, hostID uint) (*ssh.Client, error) {
	var host model.SshHost
	if err := db.First(&host, hostID).Error; err != nil {
		return nil, fmt.Errorf("SSH 主机不存在")
	}
	return dialSSH(host, box)
}

// dialSSH 用保存的主机凭证建立 SSH 连接（密码或私钥）。
func dialSSH(host model.SshHost, box *pkg.SecretBox) (*ssh.Client, error) {	cfg := &ssh.ClientConfig{
		User:            host.Username,
		Auth:            []ssh.AuthMethod{},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // 面板场景：信任用户添加的主机
		Timeout:         15 * timeSec,
	}
	switch host.AuthType {
	case "password":
		pass, err := box.Open(host.PasswordEnc)
		if err != nil {
			return nil, fmt.Errorf("密码解密失败: %w", err)
		}
		cfg.Auth = append(cfg.Auth, ssh.Password(pass))
	case "key":
		pem, err := box.Open(host.PrivateKeyEnc)
		if err != nil {
			return nil, fmt.Errorf("私钥解密失败: %w", err)
		}
		var signer ssh.Signer
		if host.PassphraseEnc != "" {
			pass, err := box.Open(host.PassphraseEnc)
			if err != nil {
				return nil, err
			}
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(pem), []byte(pass))
			if err != nil {
				return nil, fmt.Errorf("私钥解析失败: %w", err)
			}
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(pem))
			if err != nil {
				return nil, fmt.Errorf("私钥解析失败: %w", err)
			}
		}
		cfg.Auth = append(cfg.Auth, ssh.PublicKeys(signer))
	default:
		return nil, fmt.Errorf("未知认证类型: %s", host.AuthType)
	}
	addr := fmt.Sprintf("%s:%d", host.Host, orDefault(host.Port, 22))
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败: %w", err)
	}
	return client, nil
}

// sshPty 封装一个 SSH shell 通道（双向泵 + resize + 关闭）。
type sshPty struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   ioWriter
	out     ioReader
	closeFn func()
	mu      sync.Mutex
}

func newSSHPty(client *ssh.Client, cols, rows int, onOutput func([]byte), onClose func()) (*sshPty, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	term := "xterm-256color"
	if err := sess.RequestPty(term, orDefault(rows, 24), orDefault(cols, 80), nil); err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("申请 PTY 失败: %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	p := &sshPty{client: client, session: sess, stdin: stdin, out: stdout}
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				onOutput(append([]byte(nil), buf[:n]...))
			}
			if err != nil {
				// stderr 简单排空一次后结束
				_, _ = stderr.Read(make([]byte, 1024))
				onClose()
				return
			}
		}
	}()
	if err := sess.Shell(); err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("启动 shell 失败: %w", err)
	}
	slog.Debug("ssh pty 建立")
	return p, nil
}

func (p *sshPty) Write(data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.stdin.Write(data)
	return err
}

func (p *sshPty) Resize(cols, rows int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.session.WindowChange(rows, cols)
}

func (p *sshPty) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.session.Close()
	_ = p.client.Close()
}
