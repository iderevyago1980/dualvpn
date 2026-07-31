//go:build linux

package tun

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"

	"dualvpn/internal/oscmd"
)

// Create создаёт TUN-адаптер: открывает /dev/net/tun, привязывает интерфейс
// с заданным именем через ioctl TUNSETIFF, назначает адрес и поднимает
// интерфейс. Требует CAP_NET_ADMIN (админ-права).
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
	name := ifr.Name() // ядро могло скорректировать имя

	// Назначаем адрес (/32 — точечный маршрут выдаётся сервером отдельно)
	// и поднимаем интерфейс.
	if err := configureLinux(name, cfg); err != nil {
		unix.Close(fd) //nolint:errcheck
		return nil, err
	}

	return &Device{
		Name: name,
		io:   os.NewFile(uintptr(fd), "/dev/net/tun"),
	}, nil
}

// configureLinux назначает IPv4-адрес, MTU и поднимает интерфейс через
// утилиту ip (iproute2).
func configureLinux(name string, cfg Config) error {
	cmds := [][]string{
		{"ip", "addr", "add", cfg.Address + "/32", "dev", name},
		{"ip", "link", "set", "dev", name, "mtu", strconv.Itoa(cfg.MTU)},
		{"ip", "link", "set", "dev", name, "up"},
	}
	for _, argv := range cmds {
		if out, err := oscmd.Run(oscmd.DefaultTimeout, argv[0], argv[1:]...); err != nil {
			return fmt.Errorf("%v: %w (%s)", argv, err, out)
		}
	}
	return nil
}
