package logmap

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"zjpanel/backend/internal/pkg"
	"zjpanel/backend/internal/ws"
)

// Service 请求地图：增量解析 nginx access log（zjp 格式，兼容 combined），
// GeoIP 聚合国家/站点维度，环形窗口保存最近请求，WS 推送增量。

type Entry struct {
	IP      string    `json:"ip"`
	Country string    `json:"country"`
	City    string    `json:"city,omitempty"` // 城市英文名（City 库有记录时；Country 库为空）
	Lat     float64   `json:"lat,omitempty"`
	Lon     float64   `json:"lon,omitempty"`
	Site    string    `json:"site"`
	Status  int       `json:"status"`
	Bytes   uint64    `json:"bytes"`
	Method  string    `json:"method"`
	Path    string    `json:"path"`
	Time    time.Time `json:"time"`
}

type CountryAgg struct {
	Country string  `json:"country"`
	Count   int64   `json:"count"`
	Bytes   uint64  `json:"bytes"`
	Percent float64 `json:"percent"`
}

type Summary struct {
	Minutes   int              `json:"minutes"`
	Total     int64            `json:"total"`
	Bytes     uint64           `json:"bytes"`
	Errors    int64            `json:"errors"`
	Countries []CountryAgg     `json:"countries"`
	Cities    []CityAgg        `json:"cities"` // 城市级聚合（City 库有数据时）
	Sites     []SiteAgg        `json:"sites"`
	Statuses  map[string]int64 `json:"statuses"`
	Recent    []Entry          `json:"recent"`
	GeoLoaded bool             `json:"geoLoaded"`
	ServerLoc *[2]float64      `json:"serverLoc,omitempty"` // 服务器真实位置（出口 IP 经 GeoIP 反查）
}

type SiteAgg struct {
	Site  string `json:"site"`
	Count int64  `json:"count"`
	Bytes uint64 `json:"bytes"`
}

// CityAgg 城市级聚合（bucket = 国家|城市|坐标 2 位小数；City 库同城坐标一致，质心回退城市为空）。
type CityAgg struct {
	City    string  `json:"city"` // en 名；空 = 国家质心（City 库无记录）
	Country string  `json:"country"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	Count   int64   `json:"count"`
	Bytes   uint64  `json:"bytes"`
}

// zjp 格式：ip - user [time] "request" status bytes "referer" "ua" srv=site
// combined：同上去掉 srv= 尾巴
var reLog = regexp.MustCompile(`^(\S+) - (\S+) \[([^\]]+)\] "([^"]*)" (\d{3}) (\d+|-) "([^"]*)" "([^"]*)"(?: srv=(\S+))?\s*$`)

type Service struct {
	cfg     *pkg.Config
	hub     *ws.Hub
	geo     *GeoIP
	mu      sync.Mutex
	ring    []Entry
	ringN   int // 容量
	head    int
	count   int64
	pending []Entry // 待实时广播的请求条目（1s 批量）
	// 增量解析游标
	offsets map[string]int64
	stop    chan struct{}
	once    sync.Once
}

func New(cfg *pkg.Config, hub *ws.Hub) *Service {
	return &Service{
		cfg: cfg, hub: hub, geo: NewGeoIP(cfg.DataDir),
		ringN: 100000, ring: make([]Entry, 100000), offsets: map[string]int64{}, stop: make(chan struct{}),
	}
}

func (s *Service) Close() { s.once.Do(func() { close(s.stop) }) }

func (s *Service) push(e Entry) {
	s.mu.Lock()
	s.ring[s.head] = e
	s.head = (s.head + 1) % s.ringN
	s.count++
	s.pending = append(s.pending, e) // 实时流向广播队列
	s.mu.Unlock()
}

// Start 周期增量解析（5s）+ WS 推送：实时请求条目（1s）+ 聚合摘要（10s），有订阅者时。
func (s *Service) Start() {
	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-tick.C:
				s.tailLogs()
			}
		}
	}()
	// 实时流向：1s 批量推送新解析的请求条目
	go func() {
		tick := time.NewTicker(1 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-tick.C:
				s.mu.Lock()
				batch := s.pending
				s.pending = nil
				s.mu.Unlock()
				if len(batch) == 0 || s.hub.Count("logmap") == 0 {
					continue
				}
				b, _ := json.Marshal(map[string]any{"type": "entries", "data": batch})
				s.hub.Broadcast("logmap", b)
			}
		}
	}()
	go func() {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-tick.C:
				if s.hub.Count("logmap") > 0 {
					sum := s.Summary(60, 100)
					b, _ := json.Marshal(map[string]any{"type": "summary", "data": sum})
					s.hub.Broadcast("logmap", b)
				}
			}
		}
	}()
}

// tailLogs 扫描日志目录所有 *.log，按文件偏移量增量解析新行。
func (s *Service) tailLogs() {
	entries, err := os.ReadDir(s.cfg.NginxLogsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		path := filepath.Join(s.cfg.NginxLogsDir, e.Name())
		s.tailFile(path)
	}
}

func (s *Service) tailFile(path string) {
	s.mu.Lock()
	off := s.offsets[path]
	s.mu.Unlock()
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	// 日志轮转（文件变小）→ 重置游标
	if fi.Size() < off {
		s.mu.Lock()
		s.offsets[path] = 0
		s.mu.Unlock()
		off = 0
	}
	if _, err := f.Seek(off, 0); err != nil {
		return
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	newOff := off
	for sc.Scan() {
		line := sc.Text()
		newOff += int64(len(line)) + 1
		if e, ok := parseLine(line); ok {
			e.Country, e.City, e.Lat, e.Lon = s.lookupGeo(e.IP)
			s.push(e)
		}
	}
	s.mu.Lock()
	s.offsets[path] = newOff
	s.mu.Unlock()
}

func parseLine(line string) (Entry, bool) {
	m := reLog.FindStringSubmatch(line)
	if m == nil {
		return Entry{}, false
	}
	e := Entry{IP: m[1], Method: "", Path: ""}
	// request: "GET /path HTTP/1.1"
	reqParts := strings.SplitN(m[4], " ", 3)
	if len(reqParts) >= 2 {
		e.Method, e.Path = reqParts[0], reqParts[1]
	}
	status, _ := strconv.Atoi(m[5])
	e.Status = status
	if m[6] != "-" {
		b, _ := strconv.ParseUint(m[6], 10, 64)
		e.Bytes = b
	}
	if m[9] != "" {
		e.Site = m[9]
	} else {
		e.Site = "default"
	}
	if t, err := time.Parse("02/Jan/2006:15:04:05 -0700", m[3]); err == nil {
		e.Time = t
	} else {
		e.Time = time.Now()
	}
	return e, true
}

// Summary 窗口聚合（minutes 分钟内；recent 条数）。
// lookupGeo 一次性查国家/城市/经纬度（Country 库无 location 时回退国家质心，保证飞线始终有起点；
// 无 city 记录时城市为空串，前端按国家名展示）。
func (s *Service) lookupGeo(ip string) (string, string, float64, float64) {
	country, city, ll := s.geo.LookupLocation(ip)
	if country == "unknown" {
		return country, "", 0, 0
	}
	if ll[0] != 0 || ll[1] != 0 {
		return country, city, ll[0], ll[1]
	}
	if c, ok := Centroids[country]; ok {
		// Centroids 存储为 [经度, 纬度]，Entry.Lat/Lon 语义为纬度/经度，必须对调
		return country, "", c[1], c[0] // 质心回退不带城市名，避免误导
	}
	return country, "", 0, 0
}

func (s *Service) Summary(minutes, recent int) *Summary {
	if recent <= 0 {
		recent = 100
	}
	cut := time.Now().Add(-time.Duration(minutes) * time.Minute)
	s.mu.Lock()
	var entries []Entry
	for i := 0; i < s.ringN; i++ {
		idx := (s.head - 1 - i + 2*s.ringN) % s.ringN
		e := s.ring[idx]
		if e.Time.IsZero() {
			break
		}
		if e.Time.Before(cut) {
			continue
		}
		entries = append(entries, e)
	}
	s.mu.Unlock()

	sum := &Summary{
		Minutes: minutes, Countries: []CountryAgg{}, Cities: []CityAgg{}, Sites: []SiteAgg{},
		Statuses: map[string]int64{}, Recent: []Entry{}, GeoLoaded: s.geo.Loaded(),
	}
	// 服务器真实位置：出口 IP 经 GeoIP 反查。数组按 [经度, 纬度] 输出（GeoJSON/echarts 惯例，
	// 前端 serverPoint 直接消费；LookupLocation 返回的是 [纬度, 经度]，此处必须对调）
	if out := OutboundIP(); out != "" {
		if _, _, ll := s.geo.LookupLocation(out); ll[0] != 0 || ll[1] != 0 {
			sum.ServerLoc = &[2]float64{ll[1], ll[0]}
		}
	}
	countries := map[string]*CountryAgg{}
	sites := map[string]*SiteAgg{}
	cities := map[string]*CityAgg{}
	for _, e := range entries {
		sum.Total++
		sum.Bytes += e.Bytes
		if e.Status >= 400 {
			sum.Errors++
		}
		key := fmt.Sprintf("%dxx", e.Status/100)
		sum.Statuses[key+""]++
		ca := countries[e.Country]
		if ca == nil {
			ca = &CountryAgg{Country: e.Country}
			countries[e.Country] = ca
		}
		ca.Count++
		ca.Bytes += e.Bytes
		sa := sites[e.Site]
		if sa == nil {
			sa = &SiteAgg{Site: e.Site}
			sites[e.Site] = sa
		}
		sa.Count++
		sa.Bytes += e.Bytes
		// 城市级：坐标 2 位小数做 bucket（同城 IP 在 City 库中质心一致）
		ck := fmt.Sprintf("%s|%s|%.2f,%.2f", e.Country, e.City, e.Lat, e.Lon)
		cia := cities[ck]
		if cia == nil {
			cia = &CityAgg{City: e.City, Country: e.Country, Lat: e.Lat, Lon: e.Lon}
			cities[ck] = cia
		}
		cia.Count++
		cia.Bytes += e.Bytes
	}
	for _, ca := range countries {
		if sum.Total > 0 {
			ca.Percent = float64(ca.Count) / float64(sum.Total) * 100
		}
		sum.Countries = append(sum.Countries, *ca)
	}
	for _, cia := range cities {
		sum.Cities = append(sum.Cities, *cia)
	}
	sort.Slice(sum.Cities, func(i, j int) bool { return sum.Cities[i].Count > sum.Cities[j].Count })
	if len(sum.Cities) > 500 {
		sum.Cities = sum.Cities[:500]
	}
	for _, sa := range sites {
		sum.Sites = append(sum.Sites, *sa)
	}
	// 最近 N 条（新→旧）
	n := recent
	if n > len(entries) {
		n = len(entries)
	}
	sum.Recent = entries[:n]
	return sum
}

func (s *Service) GeoIPInfo() map[string]any {
	return map[string]any{
		"loaded": s.geo.Loaded(), "path": s.geo.Path(), "size": s.geo.MMDBSize(),
	}
}

// SaveMMDB 保存上传的 mmdb 并重载。
func (s *Service) SaveMMDB(data []byte) error {
	_ = os.MkdirAll(filepath.Dir(s.geo.Path()), 0o755)
	if err := os.WriteFile(s.geo.Path(), data, 0o644); err != nil {
		return err
	}
	s.geo.Reload()
	return nil
}

// DownloadMMDB 自动下载 GeoLite2-City（公开镜像，可关）。City 库为 Country 超集，
// 含经纬度（请求地图「请求方→服务器」真实流向依赖），Country 库无 location 字段。
func (s *Service) DownloadMMDB(url string) error {
	if url == "" {
		// P3TERX/GeoLite.mmdb 公开镜像（jsdelivr 旧地址已失效，改用 raw.githubusercontent 稳定分支）
		url = "https://raw.githubusercontent.com/P3TERX/GeoLite.mmdb/download/GeoLite2-City.mmdb"
	}
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}
	_ = os.MkdirAll(filepath.Dir(s.geo.Path()), 0o755)
	tmp := s.geo.Path() + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	_ = f.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.geo.Path()); err != nil {
		return err
	}
	s.geo.Reload()
	return nil
}
