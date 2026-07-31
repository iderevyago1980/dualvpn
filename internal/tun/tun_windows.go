//go:build windows

package tun

import (
	"errors"
	"fmt"
	"strconv"
	"sync"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"

	"dualvpn/internal/oscmd"
)

// wintunRingCapacity — ёмкость кольцевого буфера сессии wintun (4 МиБ).
// Должна быть степенью двойки между RingCapacityMin и RingCapacityMax.
const wintunRingCapacity = 0x400000

// Create создаёт TUN-адаптер через драйвер Wintun, открывает сессию обмена
// пакетами, назначает IPv4-адрес и MTU. Требует прав администратора и
// наличия wintun.dll рядом с исполняемым файлом. Аналог Linux-версии
// поверх /dev/net/tun.
func Create(cfg Config) (*Device, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Имя адаптера видно в «Сетевых подключениях»; tunnelType — метка драйвера.
	adapter, err := wintun.CreateAdapter(cfg.Name, "DualVPN", nil)
	if err != nil {
		return nil, fmt.Errorf("wintun: создание адаптера %q: %w (нужны админ-права и wintun.dll)", cfg.Name, err)
	}

	session, err := adapter.StartSession(wintunRingCapacity)
	if err != nil {
		_ = adapter.Close()
		return nil, fmt.Errorf("wintun: старт сессии: %w", err)
	}

	rwc := &wintunRWC{
		adapter:   adapter,
		session:   session,
		readWait:  session.ReadWaitEvent(),
		closeChan: make(chan struct{}),
	}

	// Назначаем адрес и MTU через netsh (Wintun сам IP не настраивает).
	if err := configureWindows(cfg); err != nil {
		_ = rwc.Close()
		return nil, err
	}

	return &Device{Name: cfg.Name, io: rwc}, nil
}

// configureWindows назначает статический IPv4-адрес и MTU интерфейсу через
// netsh. Требует прав администратора.
func configureWindows(cfg Config) error {
	ip, err := ParseAddress(cfg.Address)
	if err != nil {
		return err
	}
	cmds := [][]string{
		// /32 без шлюза: конкретные подсети маршрутизируются отдельно (route add).
		{"netsh", "interface", "ip", "set", "address",
			"name=" + cfg.Name, "static", ip.String(), "255.255.255.255"},
		{"netsh", "interface", "ipv4", "set", "subinterface",
			cfg.Name, "mtu=" + strconv.Itoa(cfg.MTU), "store=active"},
	}
	cmds = append(cmds, dnsCommands(cfg)...)
	for _, argv := range cmds {
		if out, err := oscmd.Run(oscmd.DefaultTimeout, argv[0], argv[1:]...); err != nil {
			return fmt.Errorf("%v: %w (%s)", argv, err, out)
		}
	}
	return nil
}

// dnsCommands назначает интерфейсу DNS-серверы, выданные шлюзом. Без этого
// в TUN-режиме имена внутренней сети не разрешаются: система продолжает
// спрашивать провайдерский DNS, которому корпоративные зоны неизвестны.
// validate=no — проверка достижимости сервера идёт до появления маршрутов и
// ложно проваливается.
func dnsCommands(cfg Config) [][]string {
	var cmds [][]string
	for i, srv := range cfg.DNS {
		if srv == "" {
			continue
		}
		if i == 0 {
			cmds = append(cmds, []string{"netsh", "interface", "ipv4", "set", "dnsservers",
				"name=" + cfg.Name, "static", srv, "primary", "validate=no"})
			continue
		}
		cmds = append(cmds, []string{"netsh", "interface", "ipv4", "add", "dnsservers",
			"name=" + cfg.Name, srv, "index=" + strconv.Itoa(i+1), "validate=no"})
	}
	return cmds
}

// wintunRWC оборачивает адаптер и сессию Wintun в io.ReadWriteCloser.
// Read блокируется до появления пакета (или закрытия), Write отправляет
// пакет в адаптер.
type wintunRWC struct {
	adapter  *wintun.Adapter
	session  wintun.Session
	readWait windows.Handle

	closeOnce sync.Once
	closeChan chan struct{}
}

// Read возвращает один IP-пакет, отправленный приложениями в TUN-интерфейс
// (направление приложение → туннель). Блокируется, пока пакет не появится
// или устройство не будет закрыто.
func (w *wintunRWC) Read(p []byte) (int, error) {
	for {
		select {
		case <-w.closeChan:
			return 0, errors.New("tun: устройство закрыто")
		default:
		}

		packet, err := w.session.ReceivePacket()
		switch {
		case err == nil:
			n := copy(p, packet)
			w.session.ReleaseReceivePacket(packet)
			if n < len(packet) {
				return n, fmt.Errorf("tun: буфер %d байт мал для пакета %d байт", len(p), len(packet))
			}
			return n, nil
		case errors.Is(err, windows.ERROR_NO_MORE_ITEMS):
			// Очередь пуста — ждём сигнала о новом пакете либо закрытия.
			if !w.waitForPacket() {
				return 0, errors.New("tun: устройство закрыто")
			}
		case errors.Is(err, windows.ERROR_HANDLE_EOF):
			return 0, errors.New("tun: сессия завершена")
		default:
			return 0, fmt.Errorf("tun: чтение пакета: %w", err)
		}
	}
}

// waitForPacket ждёт события готовности чтения Wintun или закрытия устройства.
// Возвращает false, если устройство закрыто.
func (w *wintunRWC) waitForPacket() bool {
	// 300 мс тайм-аут: периодически проверяем closeChan, даже если событие
	// не сработало (страховка от потери сигнала при гонке закрытия).
	const waitTimeoutMs = 300
	ev, err := windows.WaitForSingleObject(w.readWait, waitTimeoutMs)
	if err != nil {
		return false
	}
	select {
	case <-w.closeChan:
		return false
	default:
	}
	_ = ev // WAIT_OBJECT_0 или WAIT_TIMEOUT — в обоих случаях повторяем ReceivePacket
	return true
}

// Write отправляет один IP-пакет в TUN-интерфейс (направление туннель →
// приложение).
func (w *wintunRWC) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	packet, err := w.session.AllocateSendPacket(len(p))
	if err != nil {
		if errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			// Кольцо переполнено — пакет отбрасываем (как переполнение очереди).
			return len(p), nil
		}
		return 0, fmt.Errorf("tun: выделение send-пакета: %w", err)
	}
	copy(packet, p)
	w.session.SendPacket(packet)
	return len(p), nil
}

// Close завершает сессию и удаляет адаптер. Идемпотентен.
func (w *wintunRWC) Close() error {
	w.closeOnce.Do(func() {
		close(w.closeChan) // разбудить блокирующий Read
		w.session.End()    // ReceivePacket вернёт ошибку/EOF
		_ = w.adapter.Close()
	})
	return nil
}
