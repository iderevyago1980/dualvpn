package vpn

import "testing"

// testTunnelConfig — конфигурация тестового туннеля с указанным ID.
func testTunnelConfig(id, server, mode string) TunnelConfig {
	return TunnelConfig{
		ID: id,
		Opts: Options{
			Server:   server,
			Username: "user",
			Password: "pass",
			Mode:     mode,
		},
		Routes: []string{"192.168.1.0/24"},
	}
}

// TestManagerAddTunnel — добавление двух туннелей в менеджер.
func TestManagerAddTunnel(t *testing.T) {
	m := NewManager()

	m.AddTunnel(testTunnelConfig("astra", "vpn2.astralinux.ru", "tun"))
	m.AddTunnel(testTunnelConfig("mti", "vpn.mt-integration.ru", "socks5"))

	if len(m.tunnels) != 2 {
		t.Fatalf("в менеджере %d туннелей, ожидалось 2", len(m.tunnels))
	}
	for _, id := range []string{"astra", "mti"} {
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
	m.AddTunnel(testTunnelConfig("astra", "vpn2.astralinux.ru", "tun"))
	if err := m.Submit2FA("astra", "123456"); err == nil {
		t.Error("Submit2FA для незапущенного туннеля: ожидалась ошибка, получен nil")
	}
}

// TestManagerStatus — до запуска туннель не подключён, режим берётся из опций.
func TestManagerStatus(t *testing.T) {
	m := NewManager()
	m.AddTunnel(testTunnelConfig("astra", "vpn2.astralinux.ru", "tun"))

	connected, mode := m.Status("astra")
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
