package logmap

import (
	"testing"
	"time"

	"zjpanel/backend/internal/pkg"
)

// TestSummaryCityAgg 城市级聚合：同城去重、质心回退无城市名、按计数降序。
func TestSummaryCityAgg(t *testing.T) {
	s := New(&pkg.Config{DataDir: t.TempDir()}) // 无 mmdb，不触发查询
	s.Close()
	now := time.Now()
	push := func(country, city string, lat, lon float64, n int, bytes uint64) {
		for i := 0; i < n; i++ {
			s.push(Entry{
				IP: "1.2.3.4", Country: country, City: city, Lat: lat, Lon: lon,
				Site: "a.example.com", Status: 200, Bytes: bytes, Method: "GET",
				Path: "/", Time: now,
			})
		}
	}
	push("CN", "Shenzhen", 22.5431, 114.0579, 30, 100) // 同城 30 次 → 1 个 bucket
	push("CN", "Shenzhen", 22.5431, 114.0579, 1, 100)  // 坐标微抖动仍在 2 位小数 bucket 内（22.54,114.06）
	push("US", "Mountain View", 37.386, -122.0839, 10, 500)
	push("RU", "", 55.75, 37.62, 5, 10) // Country 库质心回退：城市为空
	push("XX", "", 0, 0, 2, 0)          // 无坐标国家（不在 Centroids）：坐标为 0

	sum := s.Summary(60, 10, false)
	if sum.Total != 48 {
		t.Fatalf("Total = %d, want 48", sum.Total)
	}
	// 路径聚合：单站点 pathCount 与（withPaths=false 时）空明细
	if len(sum.Sites) != 1 || sum.Sites[0].Site != "a.example.com" || sum.Sites[0].Count != 48 || sum.Sites[0].PathCount != 1 {
		t.Fatalf("sites 聚合异常: %+v", sum.Sites)
	}
	if len(sum.Sites[0].Paths) != 0 {
		t.Errorf("withPaths=false 不应携带路径明细: %+v", sum.Sites[0].Paths)
	}
	// withPaths=true：路径明细按次数降序
	sumP := s.Summary(60, 10, true)
	if len(sumP.Sites[0].Paths) != 1 || sumP.Sites[0].Paths[0].Path != "/" || sumP.Sites[0].Paths[0].Count != 48 {
		t.Errorf("withPaths 路径明细异常: %+v", sumP.Sites[0].Paths)
	}
	if len(sum.Cities) != 4 {
		t.Fatalf("cities bucket 数 = %d, want 4: %+v", len(sum.Cities), sum.Cities)
	}
	// 排序：按 count 降序
	if sum.Cities[0].City != "Shenzhen" || sum.Cities[0].Count != 31 {
		t.Errorf("首位应为 Shenzhen 31 次: %+v", sum.Cities[0])
	}
	if sum.Cities[1].City != "Mountain View" || sum.Cities[1].Count != 10 {
		t.Errorf("次位应为 Mountain View 10 次: %+v", sum.Cities[1])
	}
	// 质心回退 bucket：城市为空、国家 RU
	var ru *CityAgg
	for i := range sum.Cities {
		if sum.Cities[i].Country == "RU" {
			ru = &sum.Cities[i]
		}
	}
	if ru == nil || ru.City != "" || ru.Count != 5 || ru.Lat != 55.75 {
		t.Errorf("RU 质心 bucket 异常: %+v", ru)
	}
	// 无坐标国家 XX：坐标 0、城市空，仍应有 bucket
	var xx *CityAgg
	for i := range sum.Cities {
		if sum.Cities[i].Country == "XX" {
			xx = &sum.Cities[i]
		}
	}
	if xx == nil || xx.Count != 2 || xx.Lat != 0 || xx.Lon != 0 {
		t.Errorf("XX 无坐标 bucket 异常: %+v", xx)
	}
}

// TestPushFreshnessFilter 面板重启后首扫的历史条目（Time 早于服务启动时刻）
// 只进 ring 供 Summary 聚合，不得进入实时流（SSE）——实时数虚高的根因回归。
func TestPushFreshnessFilter(t *testing.T) {
	s := New(&pkg.Config{DataDir: t.TempDir()})
	s.Close()
	ch, cancel := s.Subscribe()
	defer cancel()
	old := time.Now().Add(-1 * time.Hour)
	s.push(Entry{IP: "1.1.1.1", Country: "CN", Time: old})  // 历史：只进 ring
	s.push(Entry{IP: "2.2.2.2", Country: "US", Time: time.Now()}) // 新鲜：进实时流
	select {
	case e := <-ch:
		if e.IP != "2.2.2.2" {
			t.Fatalf("实时流收到错误条目: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("新鲜条目未推送到订阅者")
	}
	select {
	case e := <-ch:
		t.Fatalf("历史条目泄漏进实时流: %+v", e)
	case <-time.After(100 * time.Millisecond):
	}
}
