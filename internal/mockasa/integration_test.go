package mockasa_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"dualvpn/internal/mockasa"
	"dualvpn/internal/vpn"
	"dualvpn/internal/vpn/sslcon"
)

// freeTCPPort возвращает свободный локальный TCP-порт.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("поиск свободного порта: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func itoa(n int) string { return strconv.Itoa(n) }

// dialWithRetry подключается к SOCKS5-порту с несколькими попытками:
// событие «connected» может опережать фактическое открытие listener.
func dialWithRetry(t *testing.T, proxyAddr, hostIP string, port uint16) net.Conn {
	t.Helper()
	ip := net.ParseIP(hostIP)
	var lastErr error
	for i := 0; i < 20; i++ {
		conn, err := net.DialTimeout("tcp", proxyAddr, time.Second)
		if err != nil {
			lastErr = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		conn.Close()
		return socksGet(t, proxyAddr, ip, port)
	}
	t.Fatalf("SOCKS5-порт %s не открылся: %v", proxyAddr, lastErr)
	return nil
}

// waitEvent ждёт событие нужного типа до таймаута.
func waitEvent(t *testing.T, ch <-chan vpn.ManagerEvent, want sslcon.EventType, timeout time.Duration) vpn.ManagerEvent {
	t.Helper()
	return waitEventMsg(t, ch, want, "", timeout)
}

// waitEventMsg ждёт событие нужного типа, чьё сообщение содержит substr
// (пустая substr — любое сообщение). run() эмитит несколько EventConnected
// подряд, поэтому готовность SOCKS5-моста отличаем по тексту сообщения.
func waitEventMsg(t *testing.T, ch <-chan vpn.ManagerEvent, want sslcon.EventType, substr string, timeout time.Duration) vpn.ManagerEvent {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-ch:
			t.Logf("[%s] %s: %s", ev.TunnelID, ev.Event.Type, ev.Event.Message)
			if ev.Event.Type == want && strings.Contains(ev.Event.Message, substr) {
				return ev
			}
			if ev.Event.Type == sslcon.EventError {
				t.Fatalf("неожиданная ошибка от туннеля: %s", ev.Event.Message)
			}
		case <-deadline:
			t.Fatalf("не дождались события %q (substr %q) за %s", want, substr, timeout)
		}
	}
}

// socksGet выполняет SOCKS5 CONNECT к ip:port и возвращает соединение.
func socksGet(t *testing.T, proxyAddr string, ip net.IP, port uint16) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 3*time.Second)
	if err != nil {
		t.Fatalf("подключение к SOCKS5 %s: %v", proxyAddr, err)
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("SOCKS5 greeting: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("SOCKS5 greeting reply: %v", err)
	}
	req := []byte{0x05, 0x01, 0x00, 0x01}
	req = append(req, ip.To4()...)
	req = binary.BigEndian.AppendUint16(req, port)
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("SOCKS5 CONNECT: %v", err)
	}
	resp := make([]byte, 10)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("SOCKS5 CONNECT reply: %v", err)
	}
	if resp[1] != 0x00 {
		t.Fatalf("SOCKS5 CONNECT отклонён, код %d", resp[1])
	}
	return conn
}

// TestSOCKS5ThroughMockGateway — полный путь одного туннеля в режиме SOCKS5:
// клиент проходит аутентификацию с 2FA на мок-шлюзе, поднимает CSTP-туннель
// и SOCKS5-мост; трафик SOCKS5-клиента доходит до echo-сервиса внутренней
// сети шлюза и возвращается.
func TestSOCKS5ThroughMockGateway(t *testing.T) {
	const (
		vpnAddr  = "10.10.0.5" // адрес, который шлюз выдаёт клиенту
		hostIP   = "10.10.0.1" // хост внутренней сети
		echoPort = 9000
		code2FA  = "424242"
	)
	srv, err := mockasa.New(mockasa.Config{
		Groups:       []string{"Group-2FA", "Basic"},
		TwoFAGroups:  map[string]string{"Group-2FA": code2FA},
		Users:        map[string]string{"alice": "secret"},
		VPNAddress:   vpnAddr,
		HostIP:       hostIP,
		SplitInclude: []string{"10.10.0.0/255.255.0.0"},
	})
	if err != nil {
		t.Fatalf("запуск мок-шлюза: %v", err)
	}
	defer srv.Close()
	if err := srv.StartEcho(echoPort); err != nil {
		t.Fatalf("echo-сервис: %v", err)
	}

	socksPort := freeTCPPort(t)
	mgr := vpn.NewManager()
	mgr.AddTunnel(vpn.TunnelConfig{
		ID: "vpn1",
		Opts: sslcon.ClientConfig{
			Host:               srv.Addr(),
			Username:           "alice",
			Password:           "secret",
			Group:              "Group-2FA",
			InsecureSkipVerify: true,
		},
		Mode:      sslcon.ModeSOCKS5,
		SocksPort: socksPort,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	events := mgr.Events()
	if err := mgr.Start(ctx, "vpn1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.StopAll()

	// Сервер запросил 2FA — передаём код
	waitEvent(t, events, sslcon.Event2FARequired, 10*time.Second)
	if err := mgr.Submit2FA("vpn1", code2FA); err != nil {
		t.Fatalf("Submit2FA: %v", err)
	}

	// Дожидаемся именно готовности SOCKS5-моста (а не промежуточных
	// "connected" вроде "установка туннеля") — по тексту сообщения
	waitEventMsg(t, events, sslcon.EventConnected, "SOCKS5", 10*time.Second)

	// SOCKS5-порт мог подняться чуть позже события — ретраим коннект
	proxyAddr := net.JoinHostPort("127.0.0.1", itoa(socksPort))
	conn := dialWithRetry(t, proxyAddr, srv.HostIP(), echoPort)
	defer conn.Close()

	msg := []byte("привет из-за VPN")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("отправка в echo: %v", err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("чтение эха: %v", err)
	}
	if string(got) != string(msg) {
		t.Errorf("эхо не совпало: %q != %q", got, msg)
	}
}

// TestWrong2FARejected — неверный код второго фактора отклоняется,
// туннель не подключается.
func TestWrong2FARejected(t *testing.T) {
	srv, err := mockasa.New(mockasa.Config{
		Groups:      []string{"Group-2FA"},
		TwoFAGroups: map[string]string{"Group-2FA": "111111"},
		Users:       map[string]string{"bob": "pw"},
		VPNAddress:  "10.20.0.5",
		HostIP:      "10.20.0.1",
	})
	if err != nil {
		t.Fatalf("запуск мок-шлюза: %v", err)
	}
	defer srv.Close()

	client := sslcon.NewClient(sslcon.ClientConfig{
		Host:               srv.Addr(),
		Username:           "bob",
		Password:           "pw",
		Group:              "Group-2FA",
		InsecureSkipVerify: true,
	})
	if err := client.InitAuth(); err != nil {
		t.Fatalf("InitAuth: %v", err)
	}
	err = client.PasswordAuth()
	if err == nil || !errors.Is(err, sslcon.ErrNeeds2FA) {
		t.Fatalf("ожидался запрос 2FA, получено: %v", err)
	}
	if err := client.Submit2FA("000000"); err == nil {
		t.Fatal("неверный 2FA-код должен быть отклонён")
	}
	_ = client.Close()
}

// waitClientEvent — то же, что waitEvent, но для канала событий одного
// клиента (тесты жизненного цикла 2FA работают без менеджера).
func waitClientEvent(t *testing.T, ch <-chan sslcon.Event, want sslcon.EventType, timeout time.Duration) sslcon.Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("канал событий закрыт до события %q", want)
			}
			t.Logf("%s: %s", ev.Type, ev.Message)
			if ev.Type == want {
				return ev
			}
			if ev.Type == sslcon.EventError {
				t.Fatalf("неожиданная ошибка от туннеля: %s", ev.Message)
			}
		case <-deadline:
			t.Fatalf("не дождались события %q за %s", want, timeout)
		}
	}
}

// TestWrong2FACodeAllowsRetry — после отклонённого кода сервер выдаёт новую
// challenge-форму, и клиент обязан снова запросить код (Event2FARequired) и
// принять правильный. Раньше повторного запроса не было: интерфейс закрывал
// окно ввода, а подключение оставалось ждать код навсегда.
func TestWrong2FACodeAllowsRetry(t *testing.T) {
	const code = "424242"
	srv, err := mockasa.New(mockasa.Config{
		Groups:      []string{"Group-2FA"},
		TwoFAGroups: map[string]string{"Group-2FA": code},
		Users:       map[string]string{"bob": "pw"},
		VPNAddress:  "10.20.0.5",
		HostIP:      "10.20.0.1",
	})
	if err != nil {
		t.Fatalf("запуск мок-шлюза: %v", err)
	}
	defer srv.Close()

	client := sslcon.NewClient(sslcon.ClientConfig{
		Host:               srv.Addr(),
		Username:           "bob",
		Password:           "pw",
		Group:              "Group-2FA",
		Mode:               sslcon.ModeSOCKS5,
		InsecureSkipVerify: true,
	})
	client.TunnelSetup = func(*sslcon.Tunnel) error { return nil }
	defer client.Disconnect() //nolint:errcheck // тест завершает туннель в любом случае

	events := client.Events()
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	waitClientEvent(t, events, sslcon.Event2FARequired, 10*time.Second)
	if err := client.Submit2FA("000000"); err == nil {
		t.Fatal("неверный 2FA-код должен быть отклонён")
	}
	// Ключевое: клиент снова просит код, а не молча висит.
	waitClientEvent(t, events, sslcon.Event2FARequired, 10*time.Second)

	if err := client.Submit2FA(code); err != nil {
		t.Fatalf("верный код после ошибки должен приниматься: %v", err)
	}
	waitClientEvent(t, events, sslcon.EventConnected, 15*time.Second)
}

// TestDisconnectDuringTwoFAWait — отказ от ввода кода должен завершать
// подключение. Раньше Disconnect не будил ожидание, горутина подключения
// висела вечно, канал событий не закрывался, и туннель нельзя было
// перезапустить («туннель уже запущен») до перезапуска приложения.
func TestDisconnectDuringTwoFAWait(t *testing.T) {
	srv, err := mockasa.New(mockasa.Config{
		Groups:      []string{"Group-2FA"},
		TwoFAGroups: map[string]string{"Group-2FA": "424242"},
		Users:       map[string]string{"bob": "pw"},
		VPNAddress:  "10.20.0.5",
		HostIP:      "10.20.0.1",
	})
	if err != nil {
		t.Fatalf("запуск мок-шлюза: %v", err)
	}
	defer srv.Close()

	client := sslcon.NewClient(sslcon.ClientConfig{
		Host:               srv.Addr(),
		Username:           "bob",
		Password:           "pw",
		Group:              "Group-2FA",
		Mode:               sslcon.ModeSOCKS5,
		InsecureSkipVerify: true,
	})
	client.TunnelSetup = func(*sslcon.Tunnel) error { return nil }

	events := client.Events()
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitClientEvent(t, events, sslcon.Event2FARequired, 10*time.Second)

	if err := client.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	// Канал событий закрывается только когда run() действительно завершилась.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return // run() завершилась — ожидание кода прервано
			}
		case <-deadline:
			t.Fatal("подключение не завершилось после Disconnect во время ожидания 2FA-кода")
		}
	}
}

// TestWrongPasswordRejected — неверный пароль отклоняется на первом шаге.
func TestWrongPasswordRejected(t *testing.T) {
	srv, err := mockasa.New(mockasa.Config{
		Groups:     []string{"Basic"},
		Users:      map[string]string{"bob": "correct"},
		VPNAddress: "10.30.0.5",
		HostIP:     "10.30.0.1",
	})
	if err != nil {
		t.Fatalf("запуск мок-шлюза: %v", err)
	}
	defer srv.Close()

	client := sslcon.NewClient(sslcon.ClientConfig{
		Host:               srv.Addr(),
		Username:           "bob",
		Password:           "wrong",
		Group:              "Basic",
		InsecureSkipVerify: true,
	})
	if err := client.InitAuth(); err != nil {
		t.Fatalf("InitAuth: %v", err)
	}
	if err := client.PasswordAuth(); err == nil {
		t.Fatal("неверный пароль должен быть отклонён")
	}
	_ = client.Close()
}
