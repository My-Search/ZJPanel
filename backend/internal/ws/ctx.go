package ws

import (
	"context"
	"time"
)

var readTimeout = 15 * time.Minute

func readCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), readTimeout)
}
