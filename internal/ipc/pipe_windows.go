//go:build windows

package ipc

import (
	"fmt"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

// pipeSDDL — права на именованный канал службы:
//
//	SY (LocalSystem) и BA (администраторы) — полный доступ;
//	IU (интерактивно вошедшие пользователи) — чтение и запись.
//
// Интерактивные пользователи, а не «все»: VPN поднимает тот, кто работает
// за машиной, а службам и сетевым сеансам управлять туннелями незачем.
const pipeSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)"

// Listen открывает именованный канал службы.
func Listen() (net.Listener, error) { return ListenNamed(PipeName) }

// ListenNamed открывает канал с указанным именем. Отдельное имя нужно
// тестам: они не должны перехватывать канал работающей службы.
func ListenNamed(name string) (net.Listener, error) {
	ln, err := winio.ListenPipe(name, &winio.PipeConfig{
		SecurityDescriptor: pipeSDDL,
		MessageMode:        false,
		InputBufferSize:    64 * 1024,
		OutputBufferSize:   64 * 1024,
	})
	if err != nil {
		return nil, fmt.Errorf("ipc: канал %s: %w", name, err)
	}
	return ln, nil
}

// Dial подключается к службе. Ошибка означает, что служба не установлена
// или не запущена — приложение в этом случае работает как раньше.
func Dial(timeout time.Duration) (*Client, error) { return DialNamed(PipeName, timeout) }

// DialNamed подключается к каналу с указанным именем (см. ListenNamed).
func DialNamed(name string, timeout time.Duration) (*Client, error) {
	conn, err := winio.DialPipe(name, &timeout)
	if err != nil {
		return nil, fmt.Errorf("ipc: служба недоступна: %w", err)
	}
	return NewClient(conn), nil
}

// Available сообщает, отвечает ли служба. Используется приложением, чтобы
// понять, доступен ли режим TUN без прав администратора.
func Available() bool {
	c, err := Dial(2 * time.Second)
	if err != nil {
		return false
	}
	defer c.Close() //nolint:errcheck // соединение открывалось только для проверки
	_, err = c.Version()
	return err == nil
}
