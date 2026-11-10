package docker

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// ContainerStat 单个容器的资源占用快照（供项目卡片聚合展示）。
type ContainerStat struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Project  string  `json:"project"` // com.docker.compose.project 标签
	CPUPct   float64 `json:"cpuPct"`
	MemBytes uint64  `json:"memBytes"`
	MemPct   float64 `json:"memPct"`
	Running  bool    `json:"running"`
}

// ContainersStats 一次性采集所有运行中容器的 CPU/内存占用。
// 主流程只做「列容器 → 并发采统计 → 汇总」，具体算法与解析封装在独立方法中。
func (s *Service) ContainersStats() ([]ContainerStat, error) {
	cli, err := s.ready()
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	list, err := cli.ContainerList(ctx, container.ListOptions{All: false})
	if err != nil {
		return nil, err
	}
	out := make([]ContainerStat, len(list))
	var wg sync.WaitGroup
	for i, c := range list {
		out[i] = newContainerStat(c)
		wg.Add(1)
		go func(idx int, id string) {
			defer wg.Done()
			cpu, mem, memPct := readOneShotStats(ctx, cli, id)
			out[idx].CPUPct, out[idx].MemBytes, out[idx].MemPct = cpu, mem, memPct
		}(i, c.ID)
	}
	wg.Wait()
	return out, nil
}

// newContainerStat 从容器摘要构造基础信息（名称/项目标签/状态）。
func newContainerStat(c types.Container) ContainerStat {
	name := ""
	if len(c.Names) > 0 {
		name = c.Names[0]
	}
	return ContainerStat{
		ID:      c.ID[:12],
		Name:    name,
		Project: c.Labels["com.docker.compose.project"],
		Running: c.State == "running",
	}
}

// readOneShotStats 读取单个容器的一帧 stats 并按 docker stats 公式计算 CPU%。
func readOneShotStats(ctx context.Context, cli *client.Client, id string) (float64, uint64, float64) {
	resp, err := cli.ContainerStatsOneShot(ctx, id)
	if err != nil {
		return 0, 0, 0
	}
	defer resp.Body.Close()
	var st container.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return 0, 0, 0
	}
	return calcCPUPct(&st), calcMemUsage(&st), calcMemPct(&st)
}

// calcCPUPct docker stats 公式：ΔcpuUsage / ΔsystemUsage × 在线核数 × 100。
func calcCPUPct(st *container.StatsResponse) float64 {
	du := float64(st.CPUStats.CPUUsage.TotalUsage - st.PreCPUStats.CPUUsage.TotalUsage)
	ds := float64(st.CPUStats.SystemUsage - st.PreCPUStats.SystemUsage)
	cores := float64(st.CPUStats.OnlineCPUs)
	if du <= 0 || ds <= 0 || cores <= 0 {
		return 0
	}
	return du / ds * cores * 100
}

// calcMemUsage 剔除 page cache 后的实际内存占用。
func calcMemUsage(st *container.StatsResponse) uint64 {
	usage := st.MemoryStats.Usage
	if cache, ok := st.MemoryStats.Stats["inactive_file"]; ok && cache < usage {
		usage -= cache
	}
	return usage
}

func calcMemPct(st *container.StatsResponse) float64 {
	limit := st.MemoryStats.Limit
	if limit == 0 {
		return 0
	}
	return float64(calcMemUsage(st)) / float64(limit) * 100
}
