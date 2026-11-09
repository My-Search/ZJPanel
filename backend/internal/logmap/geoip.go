package logmap

import (
	"net"
	"os"
	"path/filepath"
	"sync"

	maxminddb "github.com/oschwald/maxminddb-golang"
)

// GeoIP GeoLite2-Country 查询器（mmdb 缺失时全部返回 unknown）。

type GeoIP struct {
	mu    sync.RWMutex
	db    *maxminddb.Reader
	path  string
}

type mmdbCountry struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
}

func NewGeoIP(dataDir string) *GeoIP {
	g := &GeoIP{path: filepath.Join(dataDir, "geoip", "GeoLite2-Country.mmdb")}
	g.reload()
	return g
}

func (g *GeoIP) reload() {
	db, err := maxminddb.Open(g.path)
	g.mu.Lock()
	if err == nil {
		g.db = db
	} else {
		g.db = nil
	}
	g.mu.Unlock()
}

func (g *GeoIP) Loaded() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.db != nil
}

func (g *GeoIP) Path() string { return g.path }

// Reload 重新加载 mmdb（上传/下载后调用）。
func (g *GeoIP) Reload() { g.reload() }

func (g *GeoIP) LookupCountry(ipStr string) string {
	g.mu.RLock()
	db := g.db
	g.mu.RUnlock()
	if db == nil {
		return "unknown"
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return "unknown"
	}
	var rec mmdbCountry
	if err := db.Lookup(ip, &rec); err != nil || rec.Country.ISOCode == "" {
		return "unknown"
	}
	return rec.Country.ISOCode
}

func (g *GeoIP) MMDBSize() int64 {
	if fi, err := os.Stat(g.path); err == nil {
		return fi.Size()
	}
	return 0
}

// Centroids 主要国家质心坐标（请求流向图用）。
var Centroids = map[string][2]float64{
	"CN": {104.19, 35.86}, "US": {-95.71, 37.09}, "JP": {138.25, 36.2}, "KR": {127.86, 36.5},
	"SG": {103.82, 1.35}, "HK": {114.17, 22.32}, "TW": {120.96, 23.7}, "DE": {10.45, 51.16},
	"GB": {-3.43, 55.37}, "FR": {46.22, 2.21}, "RU": {105.31, 61.52}, "IN": {78.96, 20.59},
	"CA": {-106.34, 56.13}, "AU": {133.77, -25.27}, "BR": {-51.92, -10.8}, "MX": {-102.55, 23.63},
	"ID": {113.92, -0.79}, "TH": {100.99, 15.87}, "VN": {108.27, 14.05}, "MY": {101.97, 4.21},
	"PH": {121.77, 12.87}, "NL": {5.29, 52.13}, "ES": {-3.74, 40.46}, "IT": {12.56, 41.87},
	"PL": {19.14, 51.91}, "SE": {18.64, 60.12}, "NO": {8.46, 60.47}, "FI": {25.74, 61.92},
	"DK": {9.5, 56.26}, "CH": {8.22, 46.81}, "AT": {14.13, 47.51}, "BE": {4.46, 50.5},
	"IE": {-8.24, 53.41}, "PT": {-8.22, 39.39}, "GR": {21.82, 39.07}, "TR": {35.24, 38.96},
	"UA": {31.16, 48.37}, "IL": {34.85, 31.04}, "SA": {45.07, 23.88}, "AE": {53.84, 23.42},
	"PK": {69.34, 30.37}, "BD": {90.35, 23.68}, "LK": {80.77, 7.87}, "NP": {84.12, 28.39},
	"KZ": {67.7, 48.21}, "NZ": {171.18, -40.9}, "AR": {-38.41, -63.61}, "CL": {-71.96, -35.67},
	"CO": {-74.29, 4.57}, "PE": {-75.01, -9.18}, "ZA": {22.93, -30.55}, "NG": {8.67, 9.08},
	"EG": {30.8, 26.82}, "KE": {37.9, -0.02}, "MA": {-8.74, 31.79}, "DZ": {1.65, 28.03},
	"IR": {53.68, 32.42}, "IQ": {43.67, 33.22}, "MM": {95.95, 21.91}, "KH": {104.99, 12.56},
	"LA": {102.49, 19.85}, "MN": {103.84, 46.86}, "UZ": {64.58, 41.37}, "RO": {24.96, 45.94},
	"CZ": {15.47, 49.81}, "HU": {19.5, 47.16}, "BG": {25.46, 42.73}, "SK": {19.69, 48.66},
	"RS": {21.01, 44.01}, "HR": {16.44, 45.1}, "LT": {23.88, 55.17}, "LV": {24.6, 56.87},
	"EE": {25.01, 58.59}, "BY": {27.95, 53.7}, "IS": {-19.02, 64.96}, "PY": {-63.62, -22.9},
	"UY": {-55.76, -32.52}, "VE": {-66.58, 6.42}, "EC": {-78.65, -1.83}, "BO": {-64.67, -16.29},
}
