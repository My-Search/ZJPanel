package logmap

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// 服务器位置解析链：手动指定 > GeoIP 城市级记录 > 在线反查（出口 IP，结果落盘缓存）> GeoIP 原始坐标。
// 根因：GeoLite2 对仅有国家级精度记录的 IP 返回国家质心（如美国 IP 落在堪萨斯州中部），
// 不是机房真实位置——地图上服务器红点偏离就是这种情况，故 GeoIP 原始坐标只作最后兜底。

// ServerLoc 服务器位置的解析结果。
type ServerLoc struct {
	Lon    float64 `json:"lon"`
	Lat    float64 `json:"lat"`
	City   string  `json:"city,omitempty"` // 城市名（展示用；手动指定可留空）
	Source string  `json:"source"`         // manual=手动指定 online=在线反查 geoip=GeoIP
}

// serverLocState 服务器位置落盘状态（dataDir/geoip/serverloc.json）。
type serverLocState struct {
	Manual     *ServerLoc `json:"manual,omitempty"`     // 手动指定坐标（优先级最高）
	Online     *ServerLoc `json:"online,omitempty"`     // 最近一次在线反查成功结果
	OnlineIP   string     `json:"onlineIP,omitempty"`   // 反查时的出口 IP（变了才重查）
	OnlineFail string     `json:"onlineFail,omitempty"` // 最近一次失败原因（成功后清空）
	OnlineAt   time.Time  `json:"onlineAt"`             // 最近一次尝试时间（失败退避，避免每次摘要都重试打爆免费接口）
}

// onlineRetryBackoff 在线反查失败后的重试退避（成功结果永久缓存，出口 IP 变化才重查）。
const onlineRetryBackoff = 6 * time.Hour

// resolveServerLoc 按优先级解析服务器位置（结果同时预热在内存/落盘，Summary 每次调用零网络开销）。
func (s *Service) resolveServerLoc() *ServerLoc {
	s.locMu.Lock()
	defer s.locMu.Unlock()
	return s.resolveLocked(s.locStateLocked())
}

// resolveLocked 解析主体（调用方须持有 locMu）：手动 > GeoIP 城市级 > 在线反查 > GeoIP 原始坐标。
func (s *Service) resolveLocked(st *serverLocState) *ServerLoc {
	if st.Manual != nil {
		return st.Manual
	}
	ip := OutboundIP()
	if ip == "" {
		return nil
	}
	// GeoIP 有城市级记录：可信，直接用，不做在线反查（避免无谓的外部请求）
	if _, city, ll := s.geo.LookupLocation(ip); (ll[0] != 0 || ll[1] != 0) && city != "" {
		return &ServerLoc{Lon: ll[1], Lat: ll[0], City: city, Source: "geoip"}
	}
	// 在线反查：出口 IP 未变复用缓存；失败过则退避期内跳过
	if st.Online == nil || st.OnlineIP != ip {
		if st.OnlineAt.IsZero() || time.Since(st.OnlineAt) >= onlineRetryBackoff {
			loc, err := onlineLookup(ip)
			st.OnlineAt = time.Now()
			if err == nil {
				st.Online, st.OnlineIP, st.OnlineFail = loc, ip, ""
			} else {
				st.OnlineFail = err.Error()
			}
			s.saveLocStateLocked(st)
		}
	}
	if st.Online != nil && st.OnlineIP == ip {
		return st.Online
	}
	// 兜底：GeoIP 原始坐标（可能是国家质心，仅大致位置；前端标注「大致」提示）
	if _, _, ll := s.geo.LookupLocation(ip); ll[0] != 0 || ll[1] != 0 {
		return &ServerLoc{Lon: ll[1], Lat: ll[0], Source: "geoip"}
	}
	return nil
}

// SetServerLoc 手动指定服务器坐标（优先级最高，覆盖自动探测）。
func (s *Service) SetServerLoc(lon, lat float64, city string) error {
	if lon < -180 || lon > 180 || lat < -90 || lat > 90 {
		return fmt.Errorf("坐标超出范围（经度 -180~180，纬度 -90~90）")
	}
	s.locMu.Lock()
	defer s.locMu.Unlock()
	st := s.locStateLocked()
	st.Manual = &ServerLoc{Lon: lon, Lat: lat, City: city, Source: "manual"}
	return s.saveLocStateLocked(st)
}

// ClearServerLoc 清除手动指定，恢复自动探测。
func (s *Service) ClearServerLoc() error {
	s.locMu.Lock()
	defer s.locMu.Unlock()
	st := s.locStateLocked()
	st.Manual = nil
	return s.saveLocStateLocked(st)
}

// ServerLocInfo 服务器位置信息（地图页「服务器位置」弹窗：当前手动配置 + 实际生效值）。
func (s *Service) ServerLocInfo() map[string]any {
	s.locMu.Lock()
	defer s.locMu.Unlock()
	st := s.locStateLocked()
	info := map[string]any{"manual": st.Manual}
	if loc := s.resolveLocked(st); loc != nil {
		info["loc"], info["source"], info["city"] = loc, loc.Source, loc.City
	} else {
		info["loc"] = nil
	}
	return info
}

// locStateLocked 读取落盘状态（内存缓存：首次读文件，此后走内存；API 写入同步更新内存）。
func (s *Service) locStateLocked() *serverLocState {
	if s.locState != nil {
		return s.locState
	}
	st := &serverLocState{}
	if data, err := os.ReadFile(s.locStatePath()); err == nil {
		_ = json.Unmarshal(data, st)
	}
	s.locState = st
	return st
}

func (s *Service) saveLocStateLocked(st *serverLocState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.locStatePath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.locStatePath(), data, 0o644); err != nil {
		return err
	}
	s.locState = st
	return nil
}

func (s *Service) locStatePath() string {
	return filepath.Join(s.cfg.DataDir, "geoip", "serverloc.json")
}

// onlineLookup 在线反查 IP 城市（免费无 key 接口）：ipwho.is 优先（HTTPS），ip-api.com 兜底。
func onlineLookup(ip string) (*ServerLoc, error) {
	loc, err1 := lookupIPWhoIs(ip)
	if err1 == nil {
		return loc, nil
	}
	loc, err2 := lookupIPApi(ip)
	if err2 == nil {
		return loc, nil
	}
	return nil, fmt.Errorf("ipwho.is: %v; ip-api.com: %v", err1, err2)
}

func lookupIPWhoIs(ip string) (*ServerLoc, error) {
	body, err := httpGetBody("https://ipwho.is/" + ip)
	if err != nil {
		return nil, err
	}
	var r struct {
		Success   bool    `json:"success"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		City      string  `json:"city"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if !r.Success || (r.Latitude == 0 && r.Longitude == 0) {
		return nil, fmt.Errorf("查询失败: %.80s", body)
	}
	return &ServerLoc{Lon: r.Longitude, Lat: r.Latitude, City: r.City, Source: "online"}, nil
}

func lookupIPApi(ip string) (*ServerLoc, error) {
	body, err := httpGetBody("http://ip-api.com/json/" + ip + "?fields=status,message,lat,lon,city")
	if err != nil {
		return nil, err
	}
	var r struct {
		Status string  `json:"status"`
		Lat    float64 `json:"lat"`
		Lon    float64 `json:"lon"`
		City   string  `json:"city"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if r.Status != "success" || (r.Lat == 0 && r.Lon == 0) {
		return nil, fmt.Errorf("查询失败: %.80s", body)
	}
	return &ServerLoc{Lon: r.Lon, Lat: r.Lat, City: r.City, Source: "online"}, nil
}

func httpGetBody(url string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d: %.80s", resp.StatusCode, body)
	}
	return body, nil
}
