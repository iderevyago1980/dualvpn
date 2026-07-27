package mockasa_test

import (
	"testing"

	"dualvpn/internal/mockasa"
	"dualvpn/internal/vpn/sslcon"
)

// TestNoGroupSelectServerAccepts воспроизводит поведение ocserv без
// select-group: сервер не предлагает список групп и отвергает пришедший
// <group-select>. Клиент DualVPN, у которого в конфиге всегда задана группа,
// должен всё равно аутентифицироваться — т.е. НЕ слать group-select, раз
// сервер групп не предложил.
//
// Этот тест написан RED-first: до фикса клиент слал group-select безусловно,
// мок-сервер в режиме NoGroupSelect его отвергал (аналог реального 401 на
// ocserv) и PasswordAuth падал — то есть RED-фаза заодно подтверждает, что
// строгость мок-сервера работает. После фикса group-select не уходит и
// аутентификация проходит.
func TestNoGroupSelectServerAccepts(t *testing.T) {
	srv, err := mockasa.New(mockasa.Config{
		NoGroupSelect: true,
		Users:         map[string]string{"bob": "pw"},
		VPNAddress:    "10.40.0.5",
		HostIP:        "10.40.0.1",
	})
	if err != nil {
		t.Fatalf("запуск мок-шлюза: %v", err)
	}
	defer srv.Close()

	client := sslcon.NewClient(sslcon.ClientConfig{
		Host:               srv.Addr(),
		Username:           "bob",
		Password:           "pw",
		Group:              "LAB", // задана в конфиге, но сервер групп не предлагает
		InsecureSkipVerify: true,
	})
	if err := client.InitAuth(); err != nil {
		t.Fatalf("InitAuth против сервера без групп: %v", err)
	}
	if err := client.PasswordAuth(); err != nil {
		t.Fatalf("PasswordAuth против сервера без групп: %v", err)
	}
	_ = client.Close()
}
