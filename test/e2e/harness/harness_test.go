package harness

import (
	"testing"

	"dualvpn/internal/config"
)

func TestBuildConfigs(t *testing.T) {
	cfg := &config.Config{
		Tunnels: []config.Tunnel{
			{Name: "a", Endpoint: "127.0.0.1:4443", Group: "GA", Username: "u1", Password: "p1", SocksPort: 1080, TunName: "ta", Routes: []string{"192.168.90.0/24"}},
			{Name: "b", Endpoint: "127.0.0.1:4444", Group: "GB", Username: "u2", Password: "p2", SocksPort: 1081, TunName: "tb", Routes: []string{"192.168.91.0/24"}},
		},
	}
	got := BuildConfigs(cfg, "socks5", true)
	if len(got) != 2 {
		t.Fatalf("len = %d, ожидалось 2", len(got))
	}
	if got[0].ID != "a" || got[0].Opts.Host != "127.0.0.1:4443" {
		t.Fatalf("t0 = %+v", got[0])
	}
	if got[0].Opts.Group != "GA" || got[0].Opts.Username != "u1" || got[0].Opts.Password != "p1" {
		t.Fatalf("t0 opts = %+v", got[0].Opts)
	}
	if !got[0].Opts.InsecureSkipVerify {
		t.Fatalf("insecure не проброшен")
	}
	if got[0].Mode != "socks5" || got[0].SocksPort != 1080 {
		t.Fatalf("t0 mode/port = %s/%d", got[0].Mode, got[0].SocksPort)
	}
	if got[1].Opts.TunName != "tb" || len(got[1].Routes) != 1 {
		t.Fatalf("t1 = %+v", got[1])
	}
}
