// Package tun — создание и обслуживание виртуального TUN-адаптера.
//
// В TUN-режиме каждый туннель получает собственный интерфейс (dualvpn0,
// dualvpn1, ...), через который маршрутизируются подсети из конфигурации.
// Linux: /dev/net/tun + ioctl TUNSETIFF; Windows: драйвер Wintun (wintun.dll).
package tun

import (
	"fmt"
	"io"
	"net"
)

// Config — параметры создаваемого TUN-адаптера.
type Config struct {
	Name    string   // Имя интерфейса (например, dualvpn0)
	Address string   // IPv4-адрес интерфейса (например, 10.8.0.2)
	MTU     int      // MTU туннеля (обычно 1400 для AnyConnect)
	DNS     []string // DNS-серверы VPN (X-CSTP-DNS); пусто — не трогать настройки
}

// Validate проверяет корректность конфигурации адаптера.
func (c Config) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("имя TUN-интерфейса не задано")
	}
	if _, err := ParseAddress(c.Address); err != nil {
		return err
	}
	if c.MTU <= 0 {
		return fmt.Errorf("некорректный MTU %d: должен быть > 0", c.MTU)
	}
	return nil
}

// ParseAddress разбирает IPv4-адрес интерфейса.
func ParseAddress(addr string) (net.IP, error) {
	if addr == "" {
		return nil, fmt.Errorf("адрес TUN-интерфейса не задан")
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return nil, fmt.Errorf("некорректный адрес TUN-интерфейса %q", addr)
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4, nil
	}
	return nil, fmt.Errorf("адрес %q не является IPv4", addr)
}

// Device — открытый TUN-адаптер. Платформенная реализация ввода-вывода
// (Linux: /dev/net/tun; Windows: wintun-сессия) скрыта за io.ReadWriteCloser,
// который конструирует Create в соответствующем платформенном файле.
type Device struct {
	Name string // Фактическое имя интерфейса
	io   io.ReadWriteCloser
}

// Close закрывает TUN-адаптер (интерфейс удаляется ОС).
func (d *Device) Close() error {
	if d == nil || d.io == nil {
		return nil
	}
	err := d.io.Close()
	d.io = nil
	return err
}

// Read читает один IP-пакет из TUN-интерфейса.
func (d *Device) Read(p []byte) (n int, err error) {
	if d == nil || d.io == nil {
		return 0, fmt.Errorf("TUN-устройство не открыто")
	}
	return d.io.Read(p)
}

// Write записывает один IP-пакет в TUN-интерфейс.
func (d *Device) Write(p []byte) (n int, err error) {
	if d == nil || d.io == nil {
		return 0, fmt.Errorf("TUN-устройство не открыто")
	}
	return d.io.Write(p)
}
