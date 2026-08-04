package ipc

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHandler — служба в тестах: запоминает вызовы вместо настоящих
// туннелей.
type fakeHandler struct {
	mu         sync.Mutex
	connected  []ConnectParams
	twoFA      []TwoFAParams
	disconnect []string
	all        int
	connectErr error
}

func (h *fakeHandler) Connect(p ConnectParams) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.connectErr != nil {
		return h.connectErr
	}
	h.connected = append(h.connected, p)
	return nil
}

func (h *fakeHandler) Disconnect(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.disconnect = append(h.disconnect, id)
	return nil
}

func (h *fakeHandler) DisconnectAll() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.all++
	return nil
}

func (h *fakeHandler) Submit2FA(id, code string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.twoFA = append(h.twoFA, TwoFAParams{ID: id, Code: code})
	return nil
}

func (h *fakeHandler) Status() []TunnelState {
	return []TunnelState{{ID: "Офис", Connected: true}}
}

func (h *fakeHandler) Version() string { return "test" }

// newPair соединяет клиент и сервер через net.Pipe: протокол не зависит от
// транспорта, поэтому проверять его можно без именованного канала (и на
// любой платформе).
func newPair(t *testing.T, h Handler) (*Client, *Server) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	srv := NewServer(h)
	go srv.handleConn(serverConn)

	c := NewClient(clientConn)
	t.Cleanup(func() { c.Close() })
	return c, srv
}

// TestRequestResponse — базовый обмен: запросы доходят до обработчика,
// ответы возвращаются вызывающему.
func TestRequestResponse(t *testing.T) {
	h := &fakeHandler{}
	c, _ := newPair(t, h)

	if v, err := c.Version(); err != nil || v != "test" {
		t.Fatalf("Version = %q, %v", v, err)
	}

	p := ConnectParams{ID: "Офис", Host: "vpn.example.com", TunName: "dualvpn0"}
	if err := c.Connect(p); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := c.Submit2FA("Офис", "123456"); err != nil {
		t.Fatalf("Submit2FA: %v", err)
	}
	if err := c.Disconnect("Офис"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.connected) != 1 || h.connected[0].Host != "vpn.example.com" {
		t.Errorf("до службы не дошли параметры подключения: %+v", h.connected)
	}
	if len(h.twoFA) != 1 || h.twoFA[0].Code != "123456" {
		t.Errorf("код 2FA не дошёл: %+v", h.twoFA)
	}
	if len(h.disconnect) != 1 {
		t.Errorf("отключение не дошло: %+v", h.disconnect)
	}
}

// TestStatus — состояние туннелей возвращается клиенту.
func TestStatus(t *testing.T) {
	c, _ := newPair(t, &fakeHandler{})
	st, err := c.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st) != 1 || st[0].ID != "Офис" || !st[0].Connected {
		t.Errorf("состояние = %+v", st)
	}
}

// TestEventsDelivered — события туннелей доходят до приложения без запроса.
func TestEventsDelivered(t *testing.T) {
	c, srv := newPair(t, &fakeHandler{})

	// Дожидаемся регистрации клиента на сервере, иначе рассылка уйдёт
	// в пустоту (гонка старта, а не дефект протокола).
	if _, err := c.Version(); err != nil {
		t.Fatalf("Version: %v", err)
	}
	srv.Broadcast(Event{TunnelID: "Офис", Type: "connected", Message: "туннель установлен"})

	select {
	case ev := <-c.Events():
		if ev.TunnelID != "Офис" || ev.Type != "connected" {
			t.Errorf("событие = %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("событие не доставлено")
	}
}

// TestHandlerErrorReturned — ошибка службы возвращается вызывающему как есть.
func TestHandlerErrorReturned(t *testing.T) {
	h := &fakeHandler{connectErr: errFake{}}
	c, _ := newPair(t, h)

	err := c.Connect(ConnectParams{ID: "Офис", Host: "vpn.example.com"})
	if err == nil || !strings.Contains(err.Error(), "шлюз недоступен") {
		t.Fatalf("ожидалась ошибка службы, получено: %v", err)
	}
}

type errFake struct{}

func (errFake) Error() string { return "шлюз недоступен" }

// TestUnknownMethod — неизвестный метод не должен ронять службу.
func TestUnknownMethod(t *testing.T) {
	c, _ := newPair(t, &fakeHandler{})
	if err := c.call("такогоНет", nil, nil); err == nil {
		t.Fatal("неизвестный метод принят")
	}
	// Соединение остаётся рабочим.
	if _, err := c.Version(); err != nil {
		t.Errorf("после неизвестного метода связь потеряна: %v", err)
	}
}

// TestBrokenConnectionWakesCallers — обрыв связи со службой не должен
// оставлять приложение висеть в ожидании ответа.
func TestBrokenConnectionWakesCallers(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	c := NewClient(clientConn)

	serverConn.Close() //nolint:errcheck // обрыв имитируем намеренно

	done := make(chan error, 1)
	go func() { done <- c.Connect(ConnectParams{ID: "Офис", Host: "vpn.example.com"}) }()

	select {
	case err := <-done:
		if err == nil {
			t.Error("при обрыве связи вызов вернул успех")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("вызов завис после обрыва связи")
	}
}
