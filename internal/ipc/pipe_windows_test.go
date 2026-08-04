//go:build windows

package ipc

import (
	"strings"
	"testing"
	"time"
)

// broadcastHandler рассылает события прямо из обработчика запроса — так
// ведёт себя служба при отключении туннеля: остановка порождает события,
// которые уходят тому же клиенту, что прислал запрос.
type broadcastHandler struct {
	srv *Server
}

func (h *broadcastHandler) Connect(ConnectParams) error { return nil }
func (h *broadcastHandler) Disconnect(id string) error {
	for i := 0; i < 5; i++ {
		h.srv.Broadcast(Event{TunnelID: id, Type: "disconnected", Message: "туннель закрыт"})
	}
	return nil
}
func (h *broadcastHandler) DisconnectAll() error           { return nil }
func (h *broadcastHandler) Submit2FA(string, string) error { return nil }
func (h *broadcastHandler) Status() []TunnelState          { return nil }
func (h *broadcastHandler) Version() string                { return "test" }

// TestRealPipeSurvivesEventsDuringRequest — обмен по настоящему именованному
// каналу: рассылка событий во время обработки запроса не должна рвать
// соединение. Именно так выглядит отключение туннеля в бою.
func TestRealPipeSurvivesEventsDuringRequest(t *testing.T) {
	ln, err := ListenNamed(testPipeName(t))
	if err != nil {
		t.Skipf("именованный канал недоступен: %v", err)
	}
	defer ln.Close() //nolint:errcheck

	h := &broadcastHandler{}
	srv := NewServer(h)
	h.srv = srv
	go srv.Serve(ln) //nolint:errcheck // слушатель закрывается в defer

	client, err := DialNamed(testPipeName(t), 5*time.Second)
	if err != nil {
		t.Fatalf("подключение к каналу: %v", err)
	}
	defer client.Close() //nolint:errcheck

	if _, err := client.Version(); err != nil {
		t.Fatalf("Version: %v", err)
	}

	// Запрос, порождающий поток событий тому же клиенту.
	if err := client.Disconnect("Офис"); err != nil {
		t.Fatalf("Disconnect с рассылкой событий: %v", err)
	}

	// Соединение обязано остаться рабочим после этого.
	if _, err := client.Version(); err != nil {
		t.Errorf("после рассылки событий связь потеряна: %v", err)
	}
}

// TestRealPipeSurvivesIdle — служба не должна ронять соединение, пока
// приложение просто держит его открытым: туннель живёт долго, а команда
// отключения приходит через минуты после подключения.
func TestRealPipeSurvivesIdle(t *testing.T) {
	if testing.Short() {
		t.Skip("длительная проверка")
	}
	ln, err := ListenNamed(testPipeName(t))
	if err != nil {
		t.Skipf("именованный канал недоступен: %v", err)
	}
	defer ln.Close() //nolint:errcheck

	srv := NewServer(&fakeHandler{})
	go srv.Serve(ln) //nolint:errcheck

	client, err := DialNamed(testPipeName(t), 5*time.Second)
	if err != nil {
		t.Fatalf("подключение к каналу: %v", err)
	}
	defer client.Close() //nolint:errcheck

	if _, err := client.Version(); err != nil {
		t.Fatalf("Version: %v", err)
	}
	time.Sleep(45 * time.Second)
	if _, err := client.Version(); err != nil {
		t.Errorf("после простоя связь потеряна: %v", err)
	}
}

// testPipeName — отдельное имя канала на каждый тест: перехватывать канал
// работающей службы тесты не должны.
func testPipeName(t *testing.T) string {
	t.Helper()
	return `\\.\pipe\dualvpn-test-` + strings.ReplaceAll(t.Name(), "/", "-")
}
