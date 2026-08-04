package mockasa_test

import (
	"context"
	"testing"
	"time"

	"dualvpn/internal/mockasa"
	"dualvpn/internal/vpn/sslcon"
)

// TestDisconnectAfterTunnelClosed — отключение поднятого туннеля не должно
// возвращать ошибку.
//
// Закрытие туннеля закрывает то же TLS-соединение, которым владеет клиент,
// поэтому следом Close() приходил на уже закрытый сокет и возвращал
// «use of closed network connection». Пользователь, нажавший «Отключить»,
// видел это как сбой, хотя туннель закрывался нормально.
func TestDisconnectAfterTunnelClosed(t *testing.T) {
	srv, err := mockasa.New(mockasa.Config{
		Groups:     []string{"Основная"},
		Users:      map[string]string{"user": "pass"},
		VPNAddress: "10.10.0.5",
		HostIP:     "10.10.0.1",
	})
	if err != nil {
		t.Fatalf("запуск мок-шлюза: %v", err)
	}
	defer srv.Close()

	client := sslcon.NewClient(sslcon.ClientConfig{
		Host:               srv.Addr(),
		Username:           "user",
		Password:           "pass",
		Group:              "Основная",
		Mode:               sslcon.ModeSOCKS5,
		InsecureSkipVerify: true,
	})
	client.TunnelSetup = func(*sslcon.Tunnel) error { return nil }

	events := client.Events()
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitClientEvent(t, events, sslcon.EventConnected, 15*time.Second)

	if err := client.Disconnect(); err != nil {
		t.Fatalf("отключение поднятого туннеля: %v", err)
	}
	// Повторное отключение тоже должно быть спокойным: интерфейс и трей
	// могут прислать команду дважды.
	if err := client.Disconnect(); err != nil {
		t.Errorf("повторное отключение: %v", err)
	}
}
