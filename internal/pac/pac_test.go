package pac

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// Правила строятся из данных шлюза: домен и подсеть ведут в порт своего
// туннеля, всё остальное — напрямую.
func TestScriptRoutesByTunnel(t *testing.T) {
	script := Script([]Tunnel{
		{
			Name: "MT", SocksPort: 1081,
			Domains: []string{"corp.example", ".vpn2-lab.example", "6.10.in-addr.arpa"},
			Subnets: []string{"10.6.0.0/255.255.0.0"},
		},
		{
			Name: "VPN-1", SocksPort: 1080,
			Domains: []string{"lab.example"},
			Subnets: []string{"192.168.10.0/24"},
		},
	})

	want := []string{
		`dnsDomainIs(host, ".corp.example")`,
		`dnsDomainIs(host, ".vpn2-lab.example")`,
		`SOCKS5 127.0.0.1:1081`,
		`dnsDomainIs(host, ".lab.example")`,
		`SOCKS5 127.0.0.1:1080`,
		`isInNet(host, "10.6.0.0", "255.255.0.0")`,
		`isInNet(host, "192.168.10.0", "255.255.255.0")`,
		`return "DIRECT"`,
	}
	for _, w := range want {
		if !strings.Contains(script, w) {
			t.Errorf("в скрипте нет %q:\n%s", w, script)
		}
	}

	// Обратные зоны для выбора прокси бесполезны.
	if strings.Contains(script, "in-addr.arpa") {
		t.Errorf("обратная зона не должна попадать в правила:\n%s", script)
	}
	// dnsResolve разрешал бы имя системным DNS — ровно то, чего мы избегаем.
	if strings.Contains(script, "dnsResolve") {
		t.Errorf("PAC не должен вызывать dnsResolve:\n%s", script)
	}
}

// Туннель без правил и без порта не должен попадать в скрипт.
func TestScriptSkipsEmptyTunnels(t *testing.T) {
	script := Script([]Tunnel{
		{Name: "Без правил", SocksPort: 1080},
		{Name: "Без порта", Domains: []string{"corp.local"}},
	})
	if strings.Contains(script, "SOCKS5") {
		t.Errorf("прокси не должен появляться без правил и порта:\n%s", script)
	}
	if !strings.Contains(script, `return "DIRECT"`) {
		t.Errorf("скрипт должен оставаться валидным:\n%s", script)
	}
}

// Сервер отдаёт актуальный скрипт с MIME-типом PAC.
func TestServerServesScript(t *testing.T) {
	s := NewServer()
	if err := s.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	s.SetTunnels([]Tunnel{{Name: "MT", SocksPort: 1081, Domains: []string{"corp.example"}}})

	url := s.URL()
	if !strings.HasSuffix(url, "/proxy.pac") {
		t.Fatalf("неожиданный URL: %q", url)
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ns-proxy-autoconfig" {
		t.Errorf("Content-Type = %q — браузер проигнорирует такой PAC", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "127.0.0.1:1081") {
		t.Errorf("в отданном скрипте нет правила туннеля:\n%s", body)
	}

	// Набор туннелей меняется на лету — сервер обязан отдавать новое.
	s.SetTunnels(nil)
	if strings.Contains(s.Script(), "127.0.0.1:1081") {
		t.Error("после сброса туннелей правило осталось в скрипте")
	}
}
