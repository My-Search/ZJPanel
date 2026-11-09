//go:build !unix

package terminal

import "errors"

// 非 Linux 平台（本地开发）不支持本地 PTY 终端；SSH 终端不受影响。
func newLocalPty(cols, rows int, onOutput func([]byte), onClose func()) (ptyHandle, error) {
	return nil, errors.New("本地终端仅支持 Linux/容器环境")
}

type ptyHandle interface {
	Write([]byte) error
	Resize(cols, rows int) error
	Close()
}
