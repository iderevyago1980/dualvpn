package vpn

import (
	"testing"

	"dualvpn/internal/vpn/sslcon"
)

// testTunnelConfig — конфигурация тестового туннеля с указанным ID.
func testTunnelConfig(id, server, mode string) TunnelConfig {
	return TunnelConfig{
		ID: id,
		Opts: sslcon.ClientConfig{
			Host:     server,
			Username: "user",
			Password: "pass",
		},
		Routes: []string{"192.168.1.0/24"},
		Mode:   mode,
	}
}

// TestManagerAddTunnel — добавление двух туннелей в менеджер.
func TestManagerAddTunnel(t *testing.T) {
	m := NewManager()

	m.AddTunnel(testTunnelConfig("tunnel-a", "vpn1.example.com", "tun"))
	m.AddTunnel(testTunnelConfig("tunnel-b", "vpn2.example.com", "socks5"))

	if len(m.tunnels) != 2 {
		t.Fatalf("в менеджере %d туннелей, ожидалось 2", len(m.tunnels))
	}
	for _, id := range []string{"tunnel-a", "tunnel-b"} {
		if _, ok := m.tunnels[id]; !ok {
			t.Errorf("туннель %q не найден в менеджере", id)
		}
	}
}

// TestManagerSubmit2FA — передача 2FA-кода несуществующему туннелю даёт ошибку.
func TestManagerSubmit2FA(t *testing.T) {
	m := NewManager()

	if err := m.Submit2FA("нет-такого", "123456"); err == nil {
		t.Error("Submit2FA для несуществующего туннеля: ожидалась ошибка, получен nil")
	}

	// Добавлен, но не запущен — клиента нет, код передать некому.
	m.AddTunnel(testTunnelConfig("tunnel-a", "vpn1.example.com", "tun"))
	if err := m.Submit2FA("tunnel-a", "123456"); err == nil {
		t.Error("Submit2FA для незапущенного туннеля: ожидалась ошибка, получен nil")
	}
}

// TestManagerStatus — до запуска туннель не подключён, режим берётся из опций.
func TestManagerStatus(t *testing.T) {
	m := NewManager()
	m.AddTunnel(testTunnelConfig("tunnel-a", "vpn1.example.com", "tun"))

	connected, mode := m.Status("tunnel-a")
	if connected {
		t.Error("Status до запуска: connected=true, ожидалось false")
	}
	if mode != "tun" {
		t.Errorf("Status: mode=%q, ожидалось \"tun\"", mode)
	}

	// Несуществующий туннель — не подключён, режим пустой.
	connected, mode = m.Status("нет-такого")
	if connected || mode != "" {
		t.Errorf("Status несуществующего туннеля = (%v, %q), ожидалось (false, \"\")", connected, mode)
	}
}
