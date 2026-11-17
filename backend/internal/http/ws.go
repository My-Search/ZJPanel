package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gin-gonic/gin"

	"zjpanel/backend/internal/terminal"
)

// wsAccept 统一 WS 升级入口：放开 Origin 严格校验（coder/websocket v1.8.13+ 默认拒绝跨源）。
// 面板常部署在 vite dev 代理 / nginx 反代之后，Host 会被改写、Origin 保持前端地址，
// 默认校验会把合法连接一律 403。安全上不受影响：会话 Cookie 为 SameSite=Lax，
// 跨站发起的 WS 握手不会携带会话，鉴权（auth.Require）仍是硬门槛。
func wsAccept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	return websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
}

// ===================== Hub 主题订阅（metrics/sitecheck/logmap/cert-run/docker-pull） =====================

func (s *Server) registerWSRoutes() {
	s.r.GET("/api/ws", s.auth.Require(), func(c *gin.Context) {
		topic := c.Query("topic")
		conn, err := wsAccept(c.Writer, c.Request)
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
	conn    *websocket.Conn
	mu      sync.Mutex
	warm    bool      // 预热期：元数据消息发出前，广播帧先排队，保证「attached/created → 回放帧 → 广播帧」严格有序
	pending [][]byte  // 预热期排队的二进制帧
}

func (t *termConn) WriteBinary(b []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.warm {
		t.pending = append(t.pending, append([]byte(nil), b...))
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return t.conn.Write(ctx, websocket.MessageBinary, b)
}

// EndWarmup 结束预热期，按序冲刷排队的广播帧。幂等。
func (t *termConn) EndWarmup() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.warm = false
	for _, b := range t.pending {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := t.conn.Write(ctx, websocket.MessageBinary, b)
		cancel()
		if err != nil {
			break
		}
	}
	t.pending = nil
}

// sendReplay 把回放字节按 64KB 分片以二进制帧发送。
// 不走 JSON 字符串：回放可能含非法 UTF-8（终端字节流任意切分），JSON 转义会把它替换成 U+FFFD 产生乱码。
func (t *termConn) sendReplay(replay []byte) error {
	const chunk = 64 * 1024
	for len(replay) > 0 {
		n := len(replay)
		if n > chunk {
			n = chunk
		}
		if err := t.WriteBinary(replay[:n]); err != nil {
			return err
		}
		replay = replay[n:]
	}
	return nil
}

func (t *termConn) WriteText(s string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return t.conn.Write(ctx, websocket.MessageText, []byte(s))
}

func (s *Server) wsTerminal(c *gin.Context) {
	conn, err := wsAccept(c.Writer, c.Request)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	tc := &termConn{conn: conn, warm: true}
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
				tc.EndWarmup()
				continue
			}
			sess = got
			// since：客户端已消费的流偏移（刷新恢复带快照偏移 / 断线重连带已收偏移），0 表示全量回放
			sincef, _ := msg["since"].(float64)
			var since uint64
			if sincef > 0 {
				since = uint64(sincef)
			}
			r := got.Attach(tc, since)
			full := since == 0 || r.Gap
			_ = tc.WriteText(`{"type":"attached","sessionId":` + jsonString(sid) +
				`,"offset":` + strconv.FormatUint(r.Offset, 10) +
				`,"gap":` + strconv.FormatBool(r.Gap) +
				`,"replayBytes":` + strconv.Itoa(len(r.Replay)) + `}`)
			_ = tc.sendReplay(r.Replay)
			// 全量回放（无快照恢复或增量已失效）后让 TUI 整屏重画，清掉回放残影
			if len(r.Replay) > 0 && full {
				go got.RedrawDance()
			}
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
		// 元数据 + 回放帧已按序发出，冲刷预热期排队的广播帧
		tc.EndWarmup()
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
		tc.EndWarmup()
		return nil
	}
	// 先注册输出客户端再回 created，避免 shell 启动横幅丢失。
	// 回放不走 JSON 字符串：created 元数据后跟二进制回放帧（可能含非法 UTF-8）。
	r := newSess.Attach(tc, 0)
	_ = tc.WriteText(`{"type":"created","session":` + s.termInfoJSON(newSess) +
		`,"offset":` + strconv.FormatUint(r.Offset, 10) +
		`,"replayBytes":` + strconv.Itoa(len(r.Replay)) + `}`)
	_ = tc.sendReplay(r.Replay)
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

// ===================== Docker 日志跟随 =====================

func (s *Server) wsDockerLogs(c *gin.Context) {
	id := c.Param("id")
	r, tty, err := s.Docker.ContainerLogsHandle(id, true, "500")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer r.Close()
	conn, err := wsAccept(c.Writer, c.Request)
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
	conn, err := wsAccept(c.Writer, c.Request)
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
	conn, err := wsAccept(c.Writer, c.Request)
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
