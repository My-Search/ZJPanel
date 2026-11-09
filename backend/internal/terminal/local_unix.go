//go:build unix

package terminal

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// ptyHandle 统一 SSH 通道与本地 PTY 的操作接口。
type ptyHandle interface {
	Write([]byte) error
	Resize(cols, rows int) error
	Close()
}

// newLocalPty 本地终端（容器/服务器内）：bash 优先，回退 sh。
func newLocalPty(cols, rows int, onOutput func([]byte), onClose func()) (ptyHandle, error) {
	shell := "/bin/bash"
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "LANG=C.UTF-8")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(orDefault(cols, 80)), Rows: uint16(orDefault(rows, 24))})
	if err != nil {
		return nil, fmt.Errorf("启动本地终端失败: %w", err)
	}
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				onOutput(append([]byte(nil), buf[:n]...))
			}
			if err != nil {
				onClose()
				return
			}
		}
	}()
	go func() { _ = cmd.Wait() }()
	return &localHandle{ptmx: ptmx, cmd: cmd}, nil
}

type localHandle struct {
	ptmx *os.File
	cmd  *exec.Cmd
}

func (h *localHandle) Write(data []byte) error {
	_, err := h.ptmx.Write(data)
	return err
}

func (h *localHandle) Resize(cols, rows int) error {
	return pty.Setsize(h.ptmx, &pty.Winsize{Cols: uint16(orDefault(cols, 80)), Rows: uint16(orDefault(rows, 24))})
}

func (h *localHandle) Close() {
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
	}
	_ = h.ptmx.Close()
}
