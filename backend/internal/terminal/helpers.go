package terminal

import (
	"io"
	"time"
)

const timeSec = time.Second

type ioWriter = io.Writer
type ioReader = io.Reader

func orDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
