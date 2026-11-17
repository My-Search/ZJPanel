package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"zjpanel/backend/internal/buildinfo"
	"zjpanel/backend/internal/pkg"
)

// Registry 聚合工具注册表：核心工具 + 插件声明的工具（统一暴露给外部 MCP 客户端）。
// 插件工具名强制加前缀 <pluginId>_<name> 防冲突；插件禁用即整体移除。

type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(args map[string]any) (string, error)
	Owner       string // core | pluginId
}

type Registry struct {
	mu    sync.RWMutex
	tools map[string]*Tool
}

func NewRegistry() *Registry { return &Registry{tools: map[string]*Tool{}} }

func (r *Registry) Register(t *Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[t.Name]; ok {
		return fmt.Errorf("MCP 工具重名: %s", t.Name)
	}
	r.tools[t.Name] = t
	return nil
}

func (r *Registry) UnregisterByOwner(owner string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range r.tools {
		if v.Owner == owner {
			delete(r.tools, k)
		}
	}
}

func (r *Registry) List() []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Tool, 0, len(r.tools))
	for _, v := range r.tools {
		out = append(out, v)
	}
	return out
}

func (r *Registry) Get(name string) *Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
}

// ---- Streamable HTTP（JSON-RPC 2.0，手写最小实现）----

type rpcRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      any         `json:"id"`
	Result  any         `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type Server struct {
	reg *Registry
}

func NewServer(reg *Registry) *Server { return &Server{reg: reg} }

// Handler 挂载到 /mcp（API Key 鉴权由外层中间件负责）。
func (s *Server) Handler() http.HandlerFunc {
	return s.serveHTTP
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte("MCP: only POST supported (streamable http, json response)"))
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: nil, Error: &rpcError{Code: -32700, Message: "解析错误"}})
		return
	}
	// 通知类（无 id）：202 空响应
	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		proto := "2025-03-26"
		if req.Params != nil {
			if p, ok := req.Params["protocolVersion"].(string); ok && p != "" {
				proto = p
			}
		}
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": proto,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "ZJPanel", "version": buildinfo.Version},
		}})
	case "notifications/initialized", "initialized":
		w.WriteHeader(http.StatusAccepted)
	case "ping":
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "tools/list":
		tools := []map[string]any{}
		for _, t := range s.reg.List() {
			schema := t.InputSchema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": schema,
			})
		}
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": tools}})
	case "tools/call":
		name, _ := req.Params["name"].(string)
		args, _ := req.Params["arguments"].(map[string]any)
		tool := s.reg.Get(name)
		if tool == nil {
			writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
				"content": []map[string]any{{"type": "text", "text": "工具不存在: " + name}},
				"isError": true,
			}})
			return
		}
		out, err := tool.Handler(args)
		result := map[string]any{
			"content": []map[string]any{{"type": "text", "text": out}},
		}
		if err != nil {
			result["isError"] = true
			result["content"] = []map[string]any{{"type": "text", "text": out + "\n错误: " + err.Error()}}
		}
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
	default:
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "未知方法: " + req.Method}})
	}
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// ---- API Key 鉴权 ----

// ValidateKey 校验 X-Api-Key / Authorization: Bearer；返回 key 明文（供查库比对）。
func ValidateKey(r *http.Request) string {
	if k := r.Header.Get("X-Api-Key"); k != "" {
		return k
	}
	auth := r.Header.Get("Authorization")
	if len(auth) > 8 && (auth[:7] == "Bearer " || auth[:7] == "bearer ") {
		return auth[7:]
	}
	return ""
}

var _ = pkg.RandomHex
