package mockasa_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"dualvpn/internal/mockasa"
	"dualvpn/internal/vpn"
	"dualvpn/internal/vpn/sslcon"
)

// gateway — поднятый мок-шлюз с echo-сервисом и параметрами туннеля.
type gateway struct {
	srv       *mockasa.Server
	socksPort int
	echoPort  uint16
	tag       string // маркер, который echo дописывает — проверяем изоляцию туннелей
}

// newGateway поднимает мок-шлюз с 2FA и echo-сервисом внутренней сети.
func newGateway(t *testing.T, id, vpnAddr, hostIP, code2FA string) *gateway {
	t.Helper()
	srv, err := mockasa.New(mockasa.Config{
		Groups:      []string{id + "-2FA"},
		TwoFAGroups: map[string]string{id + "-2FA": code2FA},
		Users:       map[string]string{"user": "pass"},
		VPNAddress:  vpnAddr,
		HostIP:      hostIP,
	})
	if err != nil {
		t.Fatalf("[%s] запуск шлюза: %v", id, err)
	}
	t.Cleanup(func() { srv.Close() })

	const echoPort = 9000
	if err := srv.StartEcho(echoPort); err != nil {
		t.Fatalf("[%s] echo: %v", id, err)
	}
	return &gateway{srv: srv, socksPort: freeTCPPort(t), echoPort: echoPort, tag: id}
}

// TestDualTunnelsSimultaneous — два независимых туннеля работают одновременно
// через один Manager. Каждый идёт к своему мок-шлюзу с собственной 2FA,
// поднимает свой SOCKS5-прокси и достаёт echo-сервис своей внутренней сети.
// Это проверяет per-tunnel состояние форка sslcon: сессии, куки, каналы
// пакетов и netstack не пересекаются между туннелями.
func TestDualTunnelsSimultaneous(t *testing.T) {
	astra := newGateway(t, "astra", "10.10.0.5", "10.10.0.1", "111111")
	mti := newGateway(t, "mti", "10.20.0.5", "10.20.0.1", "222222")

	mgr := vpn.NewManager()
	mkCfg := func(id string, g *gateway) vpn.TunnelConfig {
		return vpn.TunnelConfig{
			ID: id,
			Opts: sslcon.ClientConfig{
				Host:               g.srv.Addr(),
				Username:           "user",
				Password:           "pass",
				Group:              id + "-2FA",
				InsecureSkipVerify: true,
			},
			Mode:      sslcon.ModeSOCKS5,
			SocksPort: g.socksPort,
		}
	}
	mgr.AddTunnel(mkCfg("astra", astra))
	mgr.AddTunnel(mkCfg("mti", mti))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	events := mgr.Events()
	mgr.StartAll(ctx)
	defer mgr.StopAll()

	// Обоим туннелям нужен свой 2FA-код. События приходят вперемешку —
	// раздаём коды по TunnelID, пока оба не запросят второй фактор.
	codes := map[string]string{"astra": "111111", "mti": "222222"}
	pending := map[string]bool{"astra": true, "mti": true}
	deadline := time.After(15 * time.Second)
	for len(pending) > 0 {
		select {
		case ev := <-events:
			t.Logf("[%s] %s: %s", ev.TunnelID, ev.Event.Type, ev.Event.Message)
			switch ev.Event.Type {
			case sslcon.Event2FARequired:
				if pending[ev.TunnelID] {
					if err := mgr.Submit2FA(ev.TunnelID, codes[ev.TunnelID]); err != nil {
						t.Fatalf("[%s] Submit2FA: %v", ev.TunnelID, err)
					}
					delete(pending, ev.TunnelID)
				}
			case sslcon.EventError:
				t.Fatalf("[%s] ошибка: %s", ev.TunnelID, ev.Event.Message)
			}
		case <-deadline:
			t.Fatalf("не все туннели запросили 2FA, осталось: %v", pending)
		}
	}

	// Проверяем каждый туннель: через его SOCKS5-порт достаём echo-хост
	// его внутренней сети и убеждаемся, что ответ пришёл от нужного шлюза.
	for _, g := range []*gateway{astra, mti} {
		proxyAddr := net.JoinHostPort("127.0.0.1", itoa(g.socksPort))
		conn := dialWithRetry(t, proxyAddr, g.srv.HostIP(), g.echoPort)

		msg := []byte(fmt.Sprintf("пакет для %s", g.tag))
		if _, err := conn.Write(msg); err != nil {
			t.Fatalf("[%s] отправка: %v", g.tag, err)
		}
		got := make([]byte, len(msg))
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatalf("[%s] чтение эха: %v", g.tag, err)
		}
		if string(got) != string(msg) {
			t.Errorf("[%s] эхо не совпало: %q != %q", g.tag, got, msg)
		}
		conn.Close()
	}

	// Кросс-проверка изоляции: SOCKS5-порт astra не должен доставать
	// внутреннюю сеть mti — маршруты и netstack у туннелей разные.
	astraProxy := net.JoinHostPort("127.0.0.1", itoa(astra.socksPort))
	if conn := tryConnect(astraProxy, mti.srv.HostIP(), mti.echoPort); conn != nil {
		conn.Close()
		t.Error("через SOCKS5 astra удалось достучаться до внутренней сети mti — туннели не изолированы")
	}
}

// tryConnect делает SOCKS5 CONNECT и возвращает соединение, если хост
// ответил, иначе nil. В отличие от socksGet не валит тест при отказе —
// используется для проверки, что соединение НЕ проходит.
func tryConnect(proxyAddr, hostIP string, port uint16) net.Conn {
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		return nil
	}
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		return nil
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		conn.Close()
		return nil
	}
	req := []byte{0x05, 0x01, 0x00, 0x01}
	req = append(req, net.ParseIP(hostIP).To4()...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil
	}
	resp := make([]byte, 10)
	if _, err := io.ReadFull(conn, resp); err != nil {
		conn.Close()
		return nil
	}
	if resp[1] != 0x00 { // REP != success
		conn.Close()
		return nil
	}
	return conn
}
