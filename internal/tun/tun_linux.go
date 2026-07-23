//go:build linux

package tun

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Create создаёт TUN-адаптер: открывает /dev/net/tun и привязывает
// интерфейс с заданным именем через ioctl TUNSETIFF.
// Требует CAP_NET_ADMIN (админ-права).
func Create(cfg Config) (*Device, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("открытие /dev/net/tun: %w", err)
	}

	// IFF_TUN — L3-туннель (IP-пакеты), IFF_NO_PI — без 4-байтового
	// префикса protocol information перед каждым пакетом.
	ifr, err := unix.NewIfreq(cfg.Name)
	if err != nil {
		unix.Close(fd) //nolint:errcheck // уже возвращаем более важную ошибку
		return nil, fmt.Errorf("имя интерфейса %q: %w", cfg.Name, err)
	}
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)

	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		unix.Close(fd) //nolint:errcheck // уже возвращаем более важную ошибку
		return nil, fmt.Errorf("ioctl TUNSETIFF (%s): %w", cfg.Name, err)
	}

	return &Device{
		Name: ifr.Name(), // ядро могло скорректировать имя
		fd:   fd,
		file: os.NewFile(uintptr(fd), "/dev/net/tun"),
	}, nil
}
