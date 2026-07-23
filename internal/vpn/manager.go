package vpn

import (
	"context"
	"fmt"
	"sync"

	"dualvpn/internal/vpn/sslcon"
)

// TunnelConfig — конфигурация одного туннеля под управлением менеджера.
type TunnelConfig struct {
	ID     string              // Уникальный идентификатор туннеля (например, "vpn1", "vpn2")
	Opts   sslcon.ClientConfig // Параметры sslcon (нативный Go-клиент)
	Routes []string            // Подсети (CIDR), маршрутизируемые через этот туннель в TUN-режиме
	Mode   string              // "tun" или "socks5"
}

// ManagerEvent — событие туннеля с указанием его идентификатора.
type ManagerEvent struct {
	TunnelID string
	Event    sslcon.Event
}

// tunnelState — текущее состояние одного туннеля внутри менеджера.
type tunnelState struct {
	cfg       TunnelConfig
	client    *sslcon.Client
	events    chan sslcon.Event // Канал событий текущего клиента (nil до запуска)
	connected bool
}

// Manager — координирует несколько одновременных VPN-туннелей:
// запуск/остановка, передача 2FA-кодов, агрегация событий.
type Manager struct {
	mu      sync.Mutex
	tunnels map[string]*tunnelState
	events  chan ManagerEvent
}

// NewManager создаёт пустой менеджер туннелей.
func NewManager() *Manager {
	return &Manager{
		tunnels: make(map[string]*tunnelState),
		events:  make(chan ManagerEvent, 64),
	}
}

// AddTunnel регистрирует туннель в менеджере (без запуска).
// Повторное добавление с тем же ID заменяет конфигурацию.
func (m *Manager) AddTunnel(cfg TunnelConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tunnels[cfg.ID] = &tunnelState{cfg: cfg}
}

// ReplaceTunnels останавливает все туннели и заменяет их набор новым.
// Канал событий сохраняется: события остановки дойдут до подписчика,
// а туннели, исчезнувшие из нового набора, будут удалены из менеджера.
func (m *Manager) ReplaceTunnels(cfgs []TunnelConfig) {
	m.StopAll()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tunnels = make(map[string]*tunnelState, len(cfgs))
	for _, c := range cfgs {
		m.tunnels[c.ID] = &tunnelState{cfg: c}
	}
}

// Events возвращает агрегированный канал событий всех туннелей.
func (m *Manager) Events() <-chan ManagerEvent {
	return m.events
}

// Start запускает один туннель по идентификатору.
// Для каждого запуска создаётся новый Client — туннель можно
// перезапускать после отключения.
func (m *Manager) Start(ctx context.Context, id string) error {
	m.mu.Lock()
	st, ok := m.tunnels[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("туннель %q не зарегистрирован", id)
	}
	if st.client != nil {
		m.mu.Unlock()
		return fmt.Errorf("туннель %q уже запущен", id)
	}
	client := sslcon.NewClient(st.cfg.Opts)
	st.client = client
	st.events = make(chan sslcon.Event, 16) // буфер для событий клиента
	go func() { // копируем события из клиента в наш канал
		for ev := range client.Events() {
			st.events <- ev
		}
		close(st.events)
	}()
	m.mu.Unlock()

	if err := client.Connect(ctx); err != nil {
		m.mu.Lock()
		st.client = nil
		st.events = nil
		m.mu.Unlock()
		return fmt.Errorf("запуск туннеля %q: %w", id, err)
	}

	go m.forwardEvents(id, client)
	return nil
}

// StartAll запускает все зарегистрированные туннели.
// Ошибки отдельных туннелей приходят событиями EventError.
func (m *Manager) StartAll(ctx context.Context) {
	m.mu.Lock()
	ids := make([]string, 0, len(m.tunnels))
	for id := range m.tunnels {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		if err := m.Start(ctx, id); err != nil {
			m.emit(id, sslcon.Event{Type: sslcon.EventError, Message: err.Error()})
		}
	}
}

// Stop останавливает туннель по идентификатору.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	st, ok := m.tunnels[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("туннель %q не зарегистрирован", id)
	}
	client := st.client
	m.mu.Unlock()

	if client == nil {
		return nil // не запущен — нечего останавливать
	}
	return client.Disconnect()
}

// StopAll останавливает все запущенные туннели.
func (m *Manager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.tunnels))
	for id := range m.tunnels {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		m.Stop(id) //nolint:errcheck // при массовой остановке ошибки отдельных туннелей не критичны
	}
}

// Submit2FA передаёт 2FA-код указанному туннелю.
func (m *Manager) Submit2FA(id, code string) error {
	m.mu.Lock()
	st, ok := m.tunnels[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("туннель %q не зарегистрирован", id)
	}
	client := st.client
	m.mu.Unlock()

	if client == nil {
		return fmt.Errorf("туннель %q не запущен — 2FA-код некому передать", id)
	}
	client.Submit2FA(code)
	return nil
}

// Status возвращает состояние туннеля: подключён ли и режим работы.
// Для неизвестного идентификатора — (false, "").
func (m *Manager) Status(id string) (connected bool, mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.tunnels[id]
	if !ok {
		return false, ""
	}
	return st.connected, st.cfg.Mode
}

// forwardEvents читает события клиента до закрытия его канала,
// обновляет состояние туннеля и пересылает события в общий канал.
func (m *Manager) forwardEvents(id string, client *sslcon.Client) {
	for ev := range client.Events() {
		m.mu.Lock()
		if st, ok := m.tunnels[id]; ok {
			switch ev.Type {
			case sslcon.EventConnected:
				st.connected = true
			case sslcon.EventDisconnected, sslcon.EventError:
				st.connected = false
			}
		}
		m.mu.Unlock()
		m.emit(id, ev)
	}

	// Канал клиента закрыт — процесс завершён, туннель можно запускать заново.
	m.mu.Lock()
	if st, ok := m.tunnels[id]; ok && st.client == client {
		st.client = nil
		st.connected = false
	}
	m.mu.Unlock()
}

// emit кладёт событие в агрегированный канал, не блокируясь при переполнении.
func (m *Manager) emit(id string, ev sslcon.Event) {
	select {
	case m.events <- ManagerEvent{TunnelID: id, Event: ev}:
	default: // потребитель отстал — событие статуса можно потерять
	}
}
