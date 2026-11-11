package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gin-gonic/gin"

	"zjpanel/backend/internal/terminal"
)

// ===================== Hub 主题订阅（metrics/sitecheck/logmap/cert-run/docker-pull） =====================

func (s *Server) registerWSRoutes() {
	s.r.GET("/api/ws", s.auth.Require(), func(c *gin.Context) {
		topic := c.Query("topic")
		conn, err := websocket.Accept(c.Writer, c.Request, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		unreg := s.hub.Register(topic, conn)
		defer unreg()
		// 只读保活（客户端不发送数据；读到关闭即退出）
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	})

	s.r.GET("/api/ws/terminal", s.auth.Require(), func(c *gin.Context) { s.wsTerminal(c) })
	s.r.GET("/api/ws/docker/logs/:id", s.auth.Require(), func(c *gin.Context) { s.wsDockerLogs(c) })
	s.r.GET("/api/ws/docker/exec/:id", s.auth.Require(), func(c *gin.Context) { s.wsDockerExec(c) })
	s.r.GET("/api/ws/docker/pull", s.auth.Require(), func(c *gin.Context) { s.wsDockerPull(c) })
}

// ===================== 终端（会话常驻 + 回放 + 多会话） =====================

type termConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (t *termConn) WriteBinary(b []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return t.conn.Write(ctx, websocket.MessageBinary, b)
}

func (t *termConn) WriteText(s string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return t.conn.Write(ctx, websocket.MessageText, []byte(s))
}

func (s *Server) wsTerminal(c *gin.Context) {
	conn, err := websocket.Accept(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	tc := &termConn{conn: conn}
	var sess *terminal.Session

	for {
		ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
		mt, data, err := conn.Read(ctx)
		cancel()
		if err != nil {
			if sess != nil {
				sess.Detach(tc)
			}
			return
		}
		if mt == websocket.MessageBinary {
			if sess != nil {
				_ = sess.Input(data)
			}
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg["type"] {
		case "create":
			// 一条连接只绑定一个会话：重复创建前先解绑旧会话，避免残留客户端。
			if sess != nil {
				sess.Detach(tc)
			}
			sess = s.createTerminalSession(tc, msg)
		case "attach":
			sid, _ := msg["sessionId"].(string)
			got := s.Term.Get(sid)
			if got == nil {
				_ = tc.WriteText(`{"type":"error","message":"会话不存在或已关闭"}`)
				continue
			}
			sess = got
			replay := got.Attach(tc)
			_ = tc.WriteText(`{"type":"attached","sessionId":` + jsonString(sid) + `,"replay":` + jsonStringBytes(replay) + `}`)
		case "resize":
			if sess != nil {
				_ = sess.Resize(int(numOf(msg["cols"])), int(numOf(msg["rows"])))
			}
		case "stats":
			// 终端底部状态栏：返回所连机器最近一次资源采样
			if sess == nil {
				continue
			}
			if st, ok := sess.Stats(); ok {
				b, _ := json.Marshal(map[string]any{"type": "stats", "sessionId": sess.ID, "stats": st})
				_ = tc.WriteText(string(b))
			} else {
				_ = tc.WriteText(`{"type":"stats","sessionId":"` + sess.ID + `","stats":null}`)
			}
		}
	}
}

// createTerminalSession 创建会话并立即把当前连接注册为输出客户端。
// 新建会话若未 Attach，命令会执行但没有任何实时输出回显（只有刷新后 attach 才看得到）。
func (s *Server) createTerminalSession(tc *termConn, msg map[string]any) *terminal.Session {
	var newSess *terminal.Session
	var err error
	kind, _ := msg["kind"].(string)
	switch kind {
	case "ssh":
		newSess, err = s.Term.CreateSSH(uint(numOf(msg["hostId"])))
	case "local":
		newSess, err = s.Term.CreateLocal()
	default:
		err = fmt.Errorf("未知终端类型: %s", kind)
	}
	if err != nil {
		_ = tc.WriteText(`{"type":"error","message":` + jsonString(err.Error()) + `}`)
		return nil
	}
	// 先注册输出客户端再回 created，避免 shell 启动横幅丢失。
	replay := newSess.Attach(tc)
	_ = tc.WriteText(`{"type":"created","session":` + s.termInfoJSON(newSess) + `,"replay":` + jsonStringBytes(replay) + `}`)
	return newSess
}

func (s *Server) termInfoJSON(sess *terminal.Session) string {
	b, _ := json.Marshal(sess.Info())
	return string(b)
}

func numOf(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	}
	return 0
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func jsonStringBytes(b []byte) string {
	out, _ := json.Marshal(string(b))
	return string(out)
}

// ===================== Docker 日志跟随 =====================

func (s *Server) wsDockerLogs(c *gin.Context) {
	id := c.Param("id")
	r, tty, err := s.Docker.ContainerLogsHandle(id, true, "500")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer r.Close()
	conn, err := websocket.Accept(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	}()
	wc := &wsWriter{conn: conn}
	if tty {
		_, _ = io.Copy(wc, r)
	} else {
		_, _ = stdcopy.StdCopy(wc, wc, r)
	}
	_ = done
}

type wsWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *wsWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := w.conn.Write(ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// ===================== Docker exec 终端 =====================

func (s *Server) wsDockerExec(c *gin.Context) {
	id := c.Param("id")
	reader, writer, closer, resizeFn, err := s.Docker.ExecHandle(id, "bash")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer closer()
	conn, err := websocket.Accept(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 8192)
		for {
			n, err := reader.Read(buf)
			if n > 0 {
				if _, werr := (&wsWriter{conn: conn}).Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
		mt, data, rerr := conn.Read(ctx)
		cancel()
		if rerr != nil {
			return
		}
		if mt == websocket.MessageBinary {
			_, _ = writer.Write(data)
			continue
		}
		var msg map[string]any
		if json.Unmarshal(data, &msg) == nil && msg["type"] == "resize" && resizeFn != nil {
			_ = resizeFn(int(numOf(msg["cols"])), int(numOf(msg["rows"])))
		}
	}
}

// ===================== 镜像拉取进度 =====================

func (s *Server) wsDockerPull(c *gin.Context) {
	conn, err := websocket.Accept(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	// 等待首帧 {"ref": "..."}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	mt, data, err := conn.Read(ctx)
	cancel()
	if err != nil || mt != websocket.MessageText {
		return
	}
	var msg struct{ Ref string }
	if json.Unmarshal(data, &msg) != nil || msg.Ref == "" {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	}()
	err = s.Docker.ImagePull(msg.Ref, func(line string) {
		_, _ = (&wsWriter{conn: conn}).Write([]byte(line))
	})
	final := map[string]any{"type": "done", "ok": err == nil}
	if err != nil {
		final["error"] = err.Error()
	}
	b, _ := json.Marshal(final)
	_, _ = (&wsWriter{conn: conn}).Write(b)
	<-done
}
