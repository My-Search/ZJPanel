package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/docker/docker/client"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
)

// Service Docker 管理（moby SDK over unix socket）。终端 exec/日志跟随由 HTTP 层基于本服务的句柄实现。

type Service struct {
	cli *client.Client
	err error
}

func New() *Service {
	s := &Service{}
	s.cli, s.err = client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if s.err != nil {
		s.err = fmt.Errorf("Docker 连接失败（检查 DOCKER_HOST/docker.sock 挂载）: %w", s.err)
	}
	return s
}

func (s *Service) ready() (*client.Client, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.cli, nil
}

func (s *Service) Available() bool { return s.err == nil }

// ---- 容器 ----

func (s *Service) ContainersList() ([]map[string]any, error) {
	cli, err := s.ready()
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	list, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(list))
	for _, c := range list {
		ports := []map[string]any{}
		for _, p := range c.Ports {
			ports = append(ports, map[string]any{
				"privatePort": p.PrivatePort, "publicPort": p.PublicPort, "type": p.Type, "ip": p.IP,
			})
		}
		name := ""
		if len(c.Names) > 0 {
			name = c.Names[0]
		}
		out = append(out, map[string]any{
			"id":      c.ID[:12],
			"names":   c.Names,
			"name":    name,
			"image":   c.Image,
			"state":   c.State,
			"status":  c.Status,
			"created": c.Created * 1000,
			"ports":   ports,
			"labels":  c.Labels,
		})
	}
	return out, nil
}

func (s *Service) ContainerAction(id, action string) (string, error) {
	cli, err := s.ready()
	if err != nil {
		return "", err
	}
	ctx := context.Background()
	switch action {
	case "start":
		return "", cli.ContainerStart(ctx, id, container.StartOptions{})
	case "stop":
		return "", cli.ContainerStop(ctx, id, container.StopOptions{})
	case "restart":
		return "", cli.ContainerRestart(ctx, id, container.StopOptions{})
	case "kill":
		return "", cli.ContainerKill(ctx, id, "SIGKILL")
	case "pause":
		return "", cli.ContainerPause(ctx, id)
	case "unpause":
		return "", cli.ContainerUnpause(ctx, id)
	case "remove":
		return "", cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: false})
	default:
		return "", fmt.Errorf("未知操作: %s", action)
	}
}

func (s *Service) ContainerInspect(id string) (map[string]any, error) {
	cli, err := s.ready()
	if err != nil {
		return nil, err
	}
	info, err := cli.ContainerInspect(context.Background(), id)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(info)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m, nil
}

// ContainerLogsHandle 日志流句柄（follow 时为持续流），HTTP 层负责泵与 TTY 解复用。
func (s *Service) ContainerLogsHandle(id string, follow bool, tail string) (io.ReadCloser, bool, error) {
	cli, err := s.ready()
	if err != nil {
		return nil, false, err
	}
	r, err := cli.ContainerLogs(context.Background(), id, container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: follow, Tail: tail, Timestamps: false,
	})
	if err != nil {
		return nil, false, err
	}
	// 判断 TTY（TTY 流无 stdcopy 头）
	insp, _ := cli.ContainerInspect(context.Background(), id)
	return r, insp.Config != nil && insp.Config.Tty, nil
}

// ExecHandle 进入容器终端：创建 exec 并返回 hijack 连接与 resize 回调。
func (s *Service) ExecHandle(id, cmd string) (io.ReadCloser, io.WriteCloser, func(), func(w, h int) error, error) {
	cli, err := s.ready()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	ctx := context.Background()
	shells := []string{cmd}
	if shells[0] == "" {
		shells = []string{"bash"}
	}
	er, err := cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd: shells, AttachStdin: true, AttachStdout: true, AttachStderr: true, Tty: true,
	})
	if err != nil {
		// bash 不存在则回退 sh
		er, err = cli.ContainerExecCreate(ctx, id, container.ExecOptions{
			Cmd: []string{"sh"}, AttachStdin: true, AttachStdout: true, AttachStderr: true, Tty: true,
		})
		if err != nil {
			return nil, nil, nil, nil, err
		}
	}
	attach, err := cli.ContainerExecAttach(ctx, er.ID, container.ExecStartOptions{Tty: true})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	resizeFn := func(w, h int) error {
		return cli.ContainerExecResize(ctx, er.ID, container.ResizeOptions{Width: uint(w), Height: uint(h)})
	}
	// attach.Reader 为 *bufio.Reader（无 Close）；包一层带 Close 的 ReadCloser
	rc := &readCloserPair{r: attach.Reader, c: attach.Conn}
	return rc, attach.Conn, func() { _ = attach.Conn.Close() }, resizeFn, nil
}

// ---- 镜像 ----

func (s *Service) ImagesList() ([]map[string]any, error) {
	cli, err := s.ready()
	if err != nil {
		return nil, err
	}
	list, err := cli.ImageList(context.Background(), image.ListOptions{All: false})
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(list))
	for _, im := range list {
		out = append(out, map[string]any{
			"id":        im.ID,
			"shortId":   im.ID[7:19],
			"tags":      im.RepoTags,
			"size":      im.Size,
			"created":   im.Created * 1000,
			"containers": im.Containers,
		})
	}
	return out, nil
}

func (s *Service) ImageRemove(ref string, force bool) ([]map[string]any, error) {
	cli, err := s.ready()
	if err != nil {
		return nil, err
	}
	items, err := cli.ImageRemove(context.Background(), ref, image.RemoveOptions{Force: force})
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, it := range items {
		out = append(out, map[string]any{"deleted": it.Deleted, "untagged": it.Untagged})
	}
	return out, nil
}

// ImagePull 拉取镜像：把 Docker 的 JSON 进度流转发到 onMsg。
func (s *Service) ImagePull(ref string, onMsg func(line string)) error {
	cli, err := s.ready()
	if err != nil {
		return err
	}
	r, err := cli.ImagePull(context.Background(), ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer r.Close()
	dec := json.NewDecoder(r)
	for {
		var msg map[string]any
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		b, _ := json.Marshal(msg)
		onMsg(string(b))
	}
	return nil
}

// ---- 网络 / 卷 ----

func (s *Service) NetworksList() ([]map[string]any, error) {
	cli, err := s.ready()
	if err != nil {
		return nil, err
	}
	list, err := cli.NetworkList(context.Background(), network.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(list))
	for _, n := range list {
		out = append(out, map[string]any{
			"id":      n.ID,
			"name":    n.Name,
			"driver":  n.Driver,
			"scope":   n.Scope,
			"internal": n.Internal,
			"created": n.Created,
		})
	}
	return out, nil
}

func (s *Service) NetworkRemove(id string) error {
	cli, err := s.ready()
	if err != nil {
		return err
	}
	return cli.NetworkRemove(context.Background(), id)
}

func (s *Service) VolumesList() ([]map[string]any, error) {
	cli, err := s.ready()
	if err != nil {
		return nil, err
	}
	res, err := cli.VolumeList(context.Background(), volume.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(res.Volumes))
	for _, v := range res.Volumes {
		out = append(out, map[string]any{
			"name":       v.Name,
			"driver":     v.Driver,
			"mountpoint": v.Mountpoint,
			"created":    v.CreatedAt,
		})
	}
	return out, nil
}

func (s *Service) VolumeRemove(name string, force bool) error {
	cli, err := s.ready()
	if err != nil {
		return err
	}
	return cli.VolumeRemove(context.Background(), name, force)
}

func (s *Service) DockerInfo() (map[string]any, error) {
	cli, err := s.ready()
	if err != nil {
		return nil, err
	}
	info, err := cli.Info(context.Background())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"version":        info.ServerVersion,
		"containers":     info.Containers,
		"containersRunning": info.ContainersRunning,
		"images":         info.Images,
		"name":           info.Name,
		"os":             info.OperatingSystem,
	}, nil
}

var pullMu sync.Mutex // 序列化镜像拉取（避免并发进度流混乱）

// readCloserPair 让 hijack 的 reader 满足 io.ReadCloser（Close 关闭底层连接）。
type readCloserPair struct {
	r *bufio.Reader
	c io.Closer
}

func (p *readCloserPair) Read(b []byte) (int, error) { return p.r.Read(b) }
func (p *readCloserPair) Close() error               { return p.c.Close() }
