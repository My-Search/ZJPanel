package certdns

import (
	"fmt"
	"os"
	"sync"

	"github.com/go-acme/lego/v5/challenge"

	"zjpanel/backend/internal/pkg"
)

// DNSRegistry DNS 提供商注册表（certd 的 dnsProviderRegistry 思路）：
// 内置提供商经 lego 注册；外部插件经 SDK 注册（Present/CleanUp 最小接口）。
// 注册项的 Fields 描述驱动前端凭证表单；Factory 用凭证配置构造 challenge.Provider。

type FieldDef struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Type     string `json:"type"` // text | password | select
	Default  string `json:"default,omitempty"`
	Help     string `json:"help,omitempty"`
	Required bool   `json:"required"`
}

type ProviderDef struct {
	Name    string                      `json:"name"`
	Title   string                      `json:"title"`
	Fields  []FieldDef                  `json:"fields"`
	Factory func(cfg map[string]string) (challenge.Provider, error) `json:"-"`
	Owner   string                      `json:"owner"` // builtin | 插件 id
}

type DNSRegistry struct {
	mu sync.RWMutex
	m  map[string]*ProviderDef
}

func NewDNSRegistry() *DNSRegistry {
	r := &DNSRegistry{m: map[string]*ProviderDef{}}
	r.registerBuiltins()
	return r
}

func (r *DNSRegistry) Register(def *ProviderDef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[def.Name] = def
}

func (r *DNSRegistry) UnregisterByOwner(owner string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range r.m {
		if v.Owner == owner {
			delete(r.m, k)
		}
	}
}

func (r *DNSRegistry) List() []*ProviderDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*ProviderDef, 0, len(r.m))
	for _, v := range r.m {
		out = append(out, v)
	}
	return out
}

func (r *DNSRegistry) Get(name string) (*ProviderDef, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.m[name]
	if !ok {
		return nil, fmt.Errorf("DNS 提供商不存在: %s", name)
	}
	return p, nil
}

func (r *DNSRegistry) registerBuiltins() {
	envFactory := func(env map[string]string) func(cfg map[string]string) (challenge.Provider, error) {
		return func(cfg map[string]string) (challenge.Provider, error) {
			for _, k := range envKeys(env) {
				_ = os.Setenv(k, cfg[k])
			}
			defer func() {
				for _, k := range envKeys(env) {
					_ = os.Unsetenv(k)
				}
			}()
			return newLegoProvider(envName(env))
		}
	}
	r.Register(&ProviderDef{
		Name: "cloudflare", Title: "Cloudflare",
		Fields: []FieldDef{{Key: "CF_DNS_API_TOKEN", Title: "API Token", Type: "password", Required: true, Help: "Cloudflare DNS API Token"}},
		Factory: envFactory(map[string]string{"CF_DNS_API_TOKEN": "cloudflare"}),
	})
	r.Register(&ProviderDef{
		Name: "alidns", Title: "阿里云 DNS",
		Fields: []FieldDef{
			{Key: "ALICLOUD_ACCESS_KEY", Title: "AccessKey ID", Type: "text", Required: true},
			{Key: "ALICLOUD_SECRET_KEY", Title: "AccessKey Secret", Type: "password", Required: true},
		},
		Factory: envFactory(map[string]string{"ALICLOUD_ACCESS_KEY": "alidns", "ALICLOUD_SECRET_KEY": "alidns"}),
	})
	r.Register(&ProviderDef{
		Name: "dnspod", Title: "腾讯云 DNSPod",
		Fields: []FieldDef{{Key: "TENCENTCLOUD_SECRET_ID", Title: "Secret ID", Type: "text", Required: true},
			{Key: "TENCENTCLOUD_SECRET_KEY", Title: "Secret Key", Type: "password", Required: true}},
		Factory: envFactory(map[string]string{"TENCENTCLOUD_SECRET_ID": "tencentcloud", "TENCENTCLOUD_SECRET_KEY": "tencentcloud"}),
	})
	r.Register(&ProviderDef{
		Name: "huaweicloud", Title: "华为云 DNS",
		Fields: []FieldDef{
			{Key: "HUAWEICLOUD_ACCESS_KEY_ID", Title: "Access Key ID", Type: "text", Required: true},
			{Key: "HUAWEICLOUD_SECRET_ACCESS_KEY", Title: "Secret Access Key", Type: "password", Required: true},
		},
		Factory: envFactory(map[string]string{"HUAWEICLOUD_ACCESS_KEY_ID": "huaweicloud", "HUAWEICLOUD_SECRET_ACCESS_KEY": "huaweicloud"}),
	})
}

func envKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// envName 返回该组环境变量对应的 lego 提供商目录名（同名 key 组 = 同一提供商）。
func envName(m map[string]string) string {
	for _, v := range m {
		return v
	}
	return ""
}

var _ = pkg.RandomHex
