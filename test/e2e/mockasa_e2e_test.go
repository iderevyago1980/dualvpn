package e2e

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"dualvpn/internal/config"
	"dualvpn/internal/mockasa"
	"dualvpn/internal/vpn/sslcon"
	"dualvpn/test/e2e/checks"
	"dualvpn/test/e2e/harness"
)

// startMock поднимает mockasa с внутренним HTTP-хостом, отдающим RemoteAddr.
func startMock(t *testing.T, hostIP, vpnAddr, split string) *mockasa.Server {
	t.Helper()
	srv, err := mockasa.New(mockasa.Config{
		Groups:       []string{"LAB"},
		Users:        map[string]string{"user": "pass"},
		VPNAddress:   vpnAddr,
		HostIP:       hostIP,
		SplitInclude: []string{split},
	})
	if err != nil {
		t.Fatalf("mockasa.New(%s): %v", hostIP, err)
	}
	h := http.NewServeMux()
	h.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("client=" + r.RemoteAddr))
	})
	if err := srv.StartHTTP(80, h); err != nil {
		srv.Close()
		t.Fatalf("StartHTTP(%s): %v", hostIP, err)
	}
	return srv
}

func TestDualTunnelSocksIsolation(t *testing.T) {
	srvA := startMock(t, "192.168.90.10", "10.90.0.2", "192.168.90.0/255.255.255.0")
	defer srvA.Close()
	srvB := startMock(t, "192.168.91.10", "10.91.0.2", "192.168.91.0/255.255.255.0")
	defer srvB.Close()

	cfg := &config.Config{
		Tunnels: []config.Tunnel{
			{Name: "a", Endpoint: srvA.Addr(), Group: "LAB", Username: "user", Password: "pass",
				SocksPort: 21080, ProbeURL: "http://192.168.90.10/"},
			{Name: "b", Endpoint: srvB.Addr(), Group: "LAB", Username: "user", Password: "pass",
				SocksPort: 21081, ProbeURL: "http://192.168.91.10/"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m, ids, err := harness.Run(ctx, harness.Options{
		Cfg: cfg, Mode: sslcon.ModeSOCKS5, Insecure: true,
		ReadyTimeout: 20 * time.Second, Logf: t.Logf,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	defer m.StopAll()
	if len(ids) != 2 {
		t.Fatalf("ids = %v", ids)
	}

	// Связность + принадлежность пулу: A виден как 10.90.0.x, B как 10.91.0.x.
	cases := []struct {
		port       int
		url        string
		wantPrefix string
	}{
		{21080, "http://192.168.90.10/", "client=10.90.0."},
		{21081, "http://192.168.91.10/", "client=10.91.0."},
	}
	for _, c := range cases {
		cl, err := checks.SocksClient(fmt.Sprintf("127.0.0.1:%d", c.port), 10*time.Second)
		if err != nil {
			t.Fatalf("socks %d: %v", c.port, err)
		}
		status, body, err := harness.GetWithRetry(cl, c.url, 15, time.Second)
		if err != nil || status != 200 {
			t.Fatalf("порт %d -> %s: status=%d err=%v", c.port, c.url, status, err)
		}
		if !strings.HasPrefix(body, c.wantPrefix) {
			t.Fatalf("порт %d: тело %q, ожидался префикс %q", c.port, body, c.wantPrefix)
		}
	}

	// Изоляция: socks-порт A не должен доставать до сети B.
	clA, err := checks.SocksClient("127.0.0.1:21080", 5*time.Second)
	if err != nil {
		t.Fatalf("socks A: %v", err)
	}
	if status, _, err := checks.GetBody(clA, "http://192.168.91.10/"); err == nil && status == 200 {
		t.Fatalf("изоляция нарушена: через туннель A достигнута сеть B")
	}
}
