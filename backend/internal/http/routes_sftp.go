package httpapi

import (
	"io"
	"net/http"
	"path"

	sftplib "github.com/pkg/sftp"
	"github.com/gin-gonic/gin"

	"zjpanel/backend/internal/sftp"
)

// ===================== SSH 主机文件管理（SFTP） =====================

func (s *Server) registerSftpRoutes() {
	g := s.r.Group("/api/sftp", s.auth.Require())
	g.GET("/hosts/:hostId/list", func(c *gin.Context) {
		p := cleanSftpPath(c.Query("path"))
		s.withSftp(c, func(cli *sftplib.Client) (map[string]any, error) {
			entries, err := sftp.List(cli, p)
			if err != nil {
				return nil, err
			}
			return map[string]any{"entries": entries}, nil
		})
	})
	g.GET("/hosts/:hostId/read", func(c *gin.Context) {
		p := cleanSftpPath(c.Query("path"))
		s.withSftp(c, func(cli *sftplib.Client) (map[string]any, error) {
			res, err := sftp.Read(cli, p)
			if err != nil {
				return nil, err
			}
			return map[string]any{"read": res}, nil
		})
	})
	g.POST("/hosts/:hostId/write", func(c *gin.Context) {
		var req struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Path == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "path 必填"})
			return
		}
		s.withSftp(c, func(cli *sftplib.Client) (map[string]any, error) {
			return nil, sftp.Write(cli, req.Path, req.Content)
		})
	})
	g.POST("/hosts/:hostId/mkdir", func(c *gin.Context) {
		var req struct{ Path string `json:"path"` }
		if err := c.ShouldBindJSON(&req); err != nil || req.Path == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "path 必填"})
			return
		}
		s.withSftp(c, func(cli *sftplib.Client) (map[string]any, error) {
			return nil, sftp.Mkdir(cli, req.Path)
		})
	})
	g.POST("/hosts/:hostId/touch", func(c *gin.Context) {
		var req struct{ Path string `json:"path"` }
		if err := c.ShouldBindJSON(&req); err != nil || req.Path == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "path 必填"})
			return
		}
		s.withSftp(c, func(cli *sftplib.Client) (map[string]any, error) {
			return nil, sftp.Touch(cli, req.Path)
		})
	})
	g.POST("/hosts/:hostId/rename", func(c *gin.Context) {
		var req struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.From == "" || req.To == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "from/to 必填"})
			return
		}
		s.withSftp(c, func(cli *sftplib.Client) (map[string]any, error) {
			return nil, sftp.Rename(cli, req.From, req.To)
		})
	})
	g.POST("/hosts/:hostId/chmod", func(c *gin.Context) {
		var req struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Path == "" || req.Mode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "path/mode 必填"})
			return
		}
		s.withSftp(c, func(cli *sftplib.Client) (map[string]any, error) {
			return nil, sftp.Chmod(cli, req.Path, req.Mode)
		})
	})
	g.POST("/hosts/:hostId/delete", func(c *gin.Context) {
		var req struct {
			Paths []string `json:"paths"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || len(req.Paths) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "paths 必填"})
			return
		}
		s.withSftp(c, func(cli *sftplib.Client) (map[string]any, error) {
			for _, p := range req.Paths {
				if err := sftp.DeleteAll(cli, p); err != nil {
					return nil, err
				}
			}
			return nil, nil
		})
	})
	g.POST("/hosts/:hostId/compress", func(c *gin.Context) {
		var req struct {
			Paths  []string `json:"paths"`
			Format string   `json:"format"` // zip | targz
			Dest   string   `json:"dest"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || len(req.Paths) == 0 || req.Dest == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "paths/dest 必填"})
			return
		}
		s.withSftp(c, func(cli *sftplib.Client) (map[string]any, error) {
			return nil, sftp.Compress(cli, req.Paths, req.Format, req.Dest)
		})
	})
	g.POST("/hosts/:hostId/decompress", func(c *gin.Context) {
		var req struct {
			Path   string `json:"path"`
			DestDir string `json:"destDir"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Path == "" || req.DestDir == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "path/destDir 必填"})
			return
		}
		s.withSftp(c, func(cli *sftplib.Client) (map[string]any, error) {
			return nil, sftp.Decompress(cli, req.Path, req.DestDir)
		})
	})
	// 上传：multipart file 字段 + path 查询参数（目标文件完整路径，覆盖写）
	g.POST("/hosts/:hostId/upload", func(c *gin.Context) {
		p := cleanSftpPath(c.Query("path"))
		fh, err := c.FormFile("file")
		if err != nil || p == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "file/path 必填"})
			return
		}
		s.withSftp(c, func(cli *sftplib.Client) (map[string]any, error) {
			src, err := fh.Open()
			if err != nil {
				return nil, err
			}
			defer src.Close()
			if dir := path.Dir(p); dir != "" && dir != "." {
				_ = cli.MkdirAll(dir)
			}
			dst, err := cli.Create(p)
			if err != nil {
				return nil, err
			}
			defer dst.Close()
			n, err := io.Copy(dst, src)
			if err != nil {
				return nil, err
			}
			return map[string]any{"size": n}, nil
		})
	})
	// 下载：流式输出（支持 Range）；连接级失败重拨一次
	g.GET("/hosts/:hostId/download", func(c *gin.Context) {
		p := cleanSftpPath(c.Query("path"))
		if p == "/" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请指定文件路径"})
			return
		}
		hostID := hostIDParam(c)
		for attempt := 0; attempt < 2; attempt++ {
			cli, err := s.Sftp.Client(hostID)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			f, err := cli.Open(p)
			if err != nil {
				if attempt == 0 {
					s.Sftp.Drop(hostID) // 缓存连接可能已失效，重拨一次
					continue
				}
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			st, err := f.Stat()
			if err != nil || st.IsDir() {
				_ = f.Close()
				c.JSON(http.StatusBadRequest, gin.H{"error": "目标不是普通文件"})
				return
			}
			name := path.Base(p)
			c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
			http.ServeContent(c.Writer, c.Request, name, st.ModTime(), f)
			return
		}
	})
}

// withSftp 取主机 SFTP 客户端执行操作；失败时 Drop 缓存重拨重试一次
// （连接可能因网络抖动/远端重启失效），应用级错误重试结果不变，无副作用。
func (s *Server) withSftp(c *gin.Context, fn func(*sftplib.Client) (map[string]any, error)) {
	hostID := hostIDParam(c)
	if hostID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的主机 ID"})
		return
	}
	res, err := s.sftpDo(hostID, fn)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	out := gin.H{"ok": true}
	for k, v := range res {
		out[k] = v
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) sftpDo(hostID uint, fn func(*sftplib.Client) (map[string]any, error)) (map[string]any, error) {
	cli, err := s.Sftp.Client(hostID)
	if err != nil {
		return nil, err
	}
	res, err := fn(cli)
	if err != nil {
		s.Sftp.Drop(hostID)
		cli2, err2 := s.Sftp.Client(hostID)
		if err2 != nil {
			return nil, err
		}
		return fn(cli2)
	}
	return res, nil
}

func hostIDParam(c *gin.Context) uint {
	var id uint
	for _, ch := range c.Param("hostId") {
		if ch < '0' || ch > '9' {
			return 0
		}
		id = id*10 + uint(ch-'0')
	}
	return id
}

// cleanSftpPath 规整路径：空值回退根目录。
func cleanSftpPath(p string) string {
	if p == "" {
		return "/"
	}
	return path.Clean("/" + p)
}
