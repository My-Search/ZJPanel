package main

import "testing"

// wsURL 是把面板地址改写成反向通道地址的唯一入口，http/https/裸域名三种写法都要正确。
func TestWSURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://127.0.0.1:9000", "ws://127.0.0.1:9000/api/agent/ws"},
		{"https://panel.example.com/", "wss://panel.example.com/api/agent/ws"},
		{"panel.example.com", "ws://panel.example.com/api/agent/ws"},
		{"http://10.0.0.5:9000/", "ws://10.0.0.5:9000/api/agent/ws"},
	}
	for _, c := range cases {
		if got := wsURL(c.in); got != c.want {
			t.Errorf("wsURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
