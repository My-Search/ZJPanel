package logmap

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	maxminddb "github.com/oschwald/maxminddb-golang"

	"zjpanel/backend/internal/pkg"
)

const maxMMDBBytes = 128 << 20 // GeoIP mmdb 最大允许 128MB

// Service 请求地图：增量解析 nginx access log（zjp 格式，兼容 combined），
// GeoIP 聚合国家/站点维度，环形窗口保存最近请求，SSE 逐条推送实时请求流。

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
	ServerLoc       *[2]float64 `json:"serverLoc,omitempty"` // 服务器位置 [经度, 纬度]（出口 IP 反查；City 库/在线反查，见 serverloc.go）
	ServerLocSource string      `json:"serverLocSource"`     // 位置来源：manual/online/geoip；空 = 未探测到（前端据此清缓存隐藏红点）
	ServerLocCity   string      `json:"serverLocCity,omitempty"` // 城市名（geoip/在线反查带出时）
}

type SiteAgg struct {
	Site      string    `json:"site"`
	Count     int64     `json:"count"`
	Bytes     uint64    `json:"bytes"`
	PathCount int       `json:"pathCount"`             // 窗口内不同路径种数（始终输出）
	Paths     []PathAgg `json:"paths,omitempty"`       // 路径明细（仅 paths=1 时输出，按次数降序）
}

// PathAgg 站点内单条请求路径的聚合（完整地图页「站点请求树」用）。
type PathAgg struct {
	Path  string `json:"path"`
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
	cfg       *pkg.Config
	geo       *GeoIP
	mu        sync.Mutex
	ring      []Entry
	ringN     int                     // 容量
	head      int
	count     int64
	startedAt time.Time               // 服务启动时刻：仅此后发生的请求才算「实时」
	subs      map[chan Entry]struct{} // SSE 实时条目订阅者
	lastQuery time.Time               // 最近一次 Summary 查询时刻（驱动扫描周期自适应）
	// 增量解析游标
	offsets  map[string]int64
	lastStat map[string]fileStat // 最近一次扫描时的文件 size/mtime：未变化直接跳过 open/read
	stop     chan struct{}
	once     sync.Once
	// 服务器位置状态（serverloc.go：手动指定/在线反查缓存，落盘 geoip/serverloc.json）
	locMu    sync.Mutex
	locState *serverLocState
}

// fileStat 用于判断日志文件是否发生变化（append 必然改变 size；轮转必然改变 size/mtime 之一）。
type fileStat struct {
	size int64
	mod  time.Time
}

func New(cfg *pkg.Config) *Service {
	ringN := cfg.LogmapRing
	if ringN <= 0 {
		ringN = 20000 // 默认 2 万条：空闲内存 ~3.4MB（此前 10 万条 ~17MB，对低占用部署偏大）
	}
	if ringN < 1000 {
		ringN = 1000
	}
	if ringN > 200000 {
		ringN = 200000
	}
	s := &Service{
		cfg: cfg, geo: NewGeoIP(cfg.DataDir),
		ringN: ringN, ring: make([]Entry, ringN), offsets: map[string]int64{}, lastStat: map[string]fileStat{}, stop: make(chan struct{}),
		startedAt: time.Now(), subs: map[chan Entry]struct{}{},
	}
	// 后台预热服务器位置（出口 IP 反查并落盘缓存）：首次打开地图页即有准确红点，
	// 不必等第一次 Summary 同步等在线接口
	go s.resolveServerLoc()
	return s
}

func (s *Service) Close() { s.once.Do(func() { close(s.stop) }) }

// Subscribe 注册实时条目订阅（SSE /api/logmap/stream）。
// 缓冲 256；消费慢时 push 侧丢最老保最新（实时流以「当前请求」为准），不阻塞日志解析。
func (s *Service) Subscribe() (<-chan Entry, func()) {
	ch := make(chan Entry, 256)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

func (s *Service) push(e Entry) {
	s.mu.Lock()
	s.ring[s.head] = e
	s.head = (s.head + 1) % s.ringN
	s.count++
	// 启动前的历史条目（面板重启后首扫整份日志、日志轮转残留）只进 ring 供聚合统计，
	// 不进实时流——否则重启瞬间整份 access log 被当作「实时请求」推送（实时数虚高的根因）。
	// startedAt 按秒截断后再比较：日志时间戳只到整秒，同一秒内启动后落盘的请求
	// 若用原始纳秒值比较会被误判为历史条目而丢失（请求流偶发断条的边角）。
	if e.Time.Before(s.startedAt.Truncate(time.Second)) {
		s.mu.Unlock()
		return
	}
	for ch := range s.subs {
		select {
		case ch <- e:
		default:
			// 订阅者消费慢：丢最老一条再投一次，保证最新请求可见
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- e:
			default:
			}
		}
	}
	s.mu.Unlock()
}

// Start 周期增量解析 access log。实时条目由 push 即时投递 SSE 订阅者（逐条，无批量延迟）；
// 聚合摘要由 /api/logmap/stream 的每个连接按 10s 周期自行推送。
// 扫描周期自适应：有 SSE 订阅者或近期有 Summary 查询（地图页/仪表盘在轮询）时 5s，
// 空闲时 30s——空闲时段不对日志目录做高频扫描。
func (s *Service) Start() {
	go func() {
		const fast, slow = 5 * time.Second, 30 * time.Second
		for {
			delay := slow
			s.mu.Lock()
			busy := len(s.subs) > 0 || (!s.lastQuery.IsZero() && time.Since(s.lastQuery) < slow+5*time.Second)
			s.mu.Unlock()
			if busy {
				delay = fast
			}
			select {
			case <-s.stop:
				return
			case <-time.After(delay):
				s.tailLogs()
			}
		}
	}()
}

// tailLogs 扫描日志目录所有 *.log，按文件偏移量增量解析新行。
// 文件 size/mtime 均未变化且已有读取游标时直接跳过（append 必然改变 size），
// 空闲时段对静止日志零 open/read。
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
		fi, err := e.Info()
		if err != nil {
			s.tailFile(path)
			continue
		}
		s.mu.Lock()
		prev, has := s.lastStat[path]
		off := s.offsets[path]
		s.mu.Unlock()
		if has && off > 0 && prev.size == fi.Size() && prev.mod.Equal(fi.ModTime()) {
			continue
		}
		s.tailFile(path)
		// 记录扫描前的 stat：若扫描期间文件又追加，下次因 stat 不匹配必然重扫
		s.mu.Lock()
		s.lastStat[path] = fileStat{size: fi.Size(), mod: fi.ModTime()}
		s.mu.Unlock()
	}
}

func (s *Service) tailFile(path string) {
	// 首扫（offsets 无记录）会从头解析整份日志：历史条目进 ring 供 Summary 聚合统计，
	// 但 push 按 startedAt 过滤，历史条目不会进入实时流（面板重启不产生「实时」洪峰）。
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

// Summary 窗口聚合（minutes 分钟内；recent 条数；withPaths 输出站点内路径明细，
// 供完整地图页「站点请求树」用——路径明细体积较大，常规摘要（仪表盘/SSE）不携带）。
func (s *Service) Summary(minutes, recent int, withPaths bool) *Summary {
	if recent <= 0 {
		recent = 100
	}
	cut := time.Now().Add(-time.Duration(minutes) * time.Minute)
	s.mu.Lock()
	s.lastQuery = time.Now() // 标记有活跃查询：扫描循环据此保持 5s 快速档
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
	// 服务器位置：手动指定 > GeoIP 城市级 > 在线反查（出口 IP，落盘缓存）> GeoIP 原始坐标
	// （可能是国家质心，仅大致位置）。数组按 [经度, 纬度] 输出（GeoJSON/echarts 惯例，
	// 前端 serverPoint 直接消费）；source 随之下发，前端据此标注红点精度
	if loc := s.resolveServerLoc(); loc != nil {
		sum.ServerLoc = &[2]float64{loc.Lon, loc.Lat}
		sum.ServerLocSource = loc.Source
		sum.ServerLocCity = loc.City
	}
	countries := map[string]*CountryAgg{}
	sites := map[string]*SiteAgg{}
	sitePaths := map[string]map[string]*PathAgg{} // 站点 → 路径 → 聚合（withPaths 时填充明细）
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
		// 路径维度：pathCount 始终累计（体积小）；明细仅在 withPaths 时保留
		pathKey := e.Path
		if pathKey == "" {
			pathKey = "-"
		}
		pm := sitePaths[e.Site]
		if pm == nil {
			pm = map[string]*PathAgg{}
			sitePaths[e.Site] = pm
		}
		pa := pm[pathKey]
		if pa == nil {
			pa = &PathAgg{Path: pathKey}
			pm[pathKey] = pa
		}
		pa.Count++
		pa.Bytes += e.Bytes
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
	// 站点输出：按次数降序（此前 map 遍历顺序随机，仪表盘「站点分布」顺序不稳定）；
	// 路径明细仅 withPaths 时携带，按次数降序、单站点最多 30 条，站点最多 100 个。
	for _, sa := range sites {
		sa.PathCount = len(sitePaths[sa.Site])
		sum.Sites = append(sum.Sites, *sa)
	}
	sort.Slice(sum.Sites, func(i, j int) bool { return sum.Sites[i].Count > sum.Sites[j].Count })
	if len(sum.Sites) > 100 {
		sum.Sites = sum.Sites[:100]
	}
	if withPaths {
		for i := range sum.Sites { // 注意：SiteAgg 是值切片，必须按下标写回
			for _, pa := range sitePaths[sum.Sites[i].Site] {
				sum.Sites[i].Paths = append(sum.Sites[i].Paths, *pa)
			}
			sort.Slice(sum.Sites[i].Paths, func(a, b int) bool { return sum.Sites[i].Paths[a].Count > sum.Sites[i].Paths[b].Count })
			if len(sum.Sites[i].Paths) > 30 {
				sum.Sites[i].Paths = sum.Sites[i].Paths[:30]
			}
		}
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

// SaveMMDB 校验上传的 mmdb 格式有效后保存并重载。
func (s *Service) SaveMMDB(data []byte) error {
	if int64(len(data)) > maxMMDBBytes {
		return fmt.Errorf("GeoIP 文件过大（上限 128MB）")
	}
	_ = os.MkdirAll(filepath.Dir(s.geo.Path()), 0o755)
	tmp := s.geo.Path() + ".upload.tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := validateMMDBFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.geo.Path()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	s.geo.Reload()
	return nil
}

func validateMMDBFile(p string) error {
	db, err := maxminddb.Open(p)
	if err != nil {
		return fmt.Errorf("无效的 MaxMind mmdb 文件: %w", err)
	}
	_ = db.Close()
	return nil
}

// validateDownloadURL 校验 GeoIP 下载地址（仅允许 HTTPS 公网地址，防 SSRF 探测内网/元数据服务）。
func validateDownloadURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" {
		return fmt.Errorf("仅允许 https:// 公网下载地址")
	}
	ips, err := net.LookupIP(u.Hostname())
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("无法解析下载域名: %s", u.Hostname())
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("禁止从内网或本机地址下载: %s", ip.String())
		}
	}
	return nil
}

// DownloadMMDB 自动下载 GeoLite2-City（公开镜像，可关）。City 库为 Country 超集，
// 含经纬度（请求地图「请求方→服务器」真实流向依赖），Country 库无 location 字段。
func (s *Service) DownloadMMDB(rawURL string) error {
	if rawURL == "" {
		// P3TERX/GeoLite.mmdb 公开镜像（jsdelivr 旧地址已失效，改用 raw.githubusercontent 稳定分支）
		rawURL = "https://raw.githubusercontent.com/P3TERX/GeoLite.mmdb/download/GeoLite2-City.mmdb"
	}
	if err := validateDownloadURL(rawURL); err != nil {
		return err
	}
	client := &http.Client{
		Timeout: 120 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("重定向次数过多")
			}
			return validateDownloadURL(req.URL.String())
		},
	}
	resp, err := client.Get(rawURL)
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
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxMMDBBytes+1))
	_ = f.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if n > maxMMDBBytes {
		_ = os.Remove(tmp)
		return fmt.Errorf("下载文件超出 128MB 限制")
	}
	if err := validateMMDBFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.geo.Path()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	s.geo.Reload()
	return nil
}
