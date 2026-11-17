package certdns

import (
	"fmt"

	// 内置 lego DNS 提供商（Factory 通过环境变量构造）
	"github.com/go-acme/lego/v5/challenge"
	"github.com/go-acme/lego/v5/providers/dns/alidns"
	"github.com/go-acme/lego/v5/providers/dns/cloudflare"
	"github.com/go-acme/lego/v5/providers/dns/tencentcloud"
	"github.com/go-acme/lego/v5/providers/dns/huaweicloud"
)

// newLegoProvider 按提供商名构造 lego DNS provider（依赖已设置好的环境变量）。
func newLegoProvider(name string) (challenge.Provider, error) {
	switch name {
	case "cloudflare":
		return cloudflare.NewDNSProvider()
	case "alidns":
		return alidns.NewDNSProvider()
	case "dnspod":
		return tencentcloud.NewDNSProvider()
	case "huaweicloud":
		return huaweicloud.NewDNSProvider()
	default:
		return nil, fmt.Errorf("未知内置 DNS 提供商: %s", name)
	}
}
