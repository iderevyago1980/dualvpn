package vpn

import (
	"context"
	"fmt"
	"net"
	"sort"
	"sync"

	"dualvpn/internal/pac"
	"dualvpn/internal/socks5"
	"dualvpn/internal/vpn/sslcon"
)

// TunnelConfig — конфигурация одного туннеля под управлением менеджера.
type TunnelConfig struct {
	ID        string              // Уникальный идентификатор туннеля (например, "vpn1", "vpn2")
	Opts      sslcon.ClientConfig // Параметры sslcon (нативный Go-клиент)
	Routes    []string            // Подсети (CIDR), маршрутизируемые через этот туннель в TUN-режиме
	Mode      string              // "tun" или "socks5"
	SocksPort int                 // Локальный порт SOCKS5-прокси (режим socks5)
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
	bridge    *socks5.Bridge // SOCKS5-мост поверх gVisor netstack (режим socks5)
	connected bool

	// pacRule — правила автонастройки прокси для этого туннеля (зоны и
	// подсети от шлюза). Заполняется при подключении, снимается при
	// отключении: PAC должен направлять браузер только в живой туннель.
	pacRule *pac.Tunnel
}

// Manager — координирует несколько одновременных VPN-туннелей:
// запуск/остановка, передача 2FA-кодов, агрегация событий.
type Manager struct {
	mu      sync.Mutex
	tunnels map[string]*tunnelState
	events  chan ManagerEvent
	pac     *pac.Server // необязательный: nil, пока PAC не включён

	// pacChanged — необязательный обработчик изменения правил PAC
	// (подключение/отключение туннеля). Нужен потребителю, который
	// настраивает системный прокси: содержимое скрипта поменялось, и
	// настройку приходится переприменять.
	pacChanged func()
}

// NewManager создаёт пустой менеджер туннелей.
func NewManager() *Manager {
	return &Manager{
		tunnels: make(map[string]*tunnelState),
		events:  make(chan ManagerEvent, 64),
	}
}

// EnablePAC поднимает раздачу PAC-файла на 127.0.0.1:port (0 — свободный
// порт) и возвращает URL для браузера. Смысл имеет только в режиме SOCKS5:
// PAC направляет браузер в тот туннель, которому принадлежит домен.
func (m *Manager) EnablePAC(port int) (string, error) {
	srv := pac.NewServer()
	if err := srv.Start(port); err != nil {
		return "", err
	}
	m.mu.Lock()
	old := m.pac
	m.pac = srv
	m.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	m.refreshPAC()
	return srv.URL(), nil
}

// DisablePAC останавливает раздачу PAC-файла. Идемпотентна: если раздача не
// запущена, возвращает nil. Нужна при переходе в режим TUN, где PAC не имеет
// смысла — трафик идёт по маршрутам системы.
func (m *Manager) DisablePAC() error {
	m.mu.Lock()
	srv := m.pac
	m.pac = nil
	m.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Close()
}

// PACURL возвращает адрес PAC-файла ("" — раздача не включена).
func (m *Manager) PACURL() string {
	m.mu.Lock()
	srv := m.pac
	m.mu.Unlock()
	if srv == nil {
		return ""
	}
	return srv.URL()
}

// PACScript возвращает текущий текст PAC-скрипта (для UI и диагностики).
func (m *Manager) PACScript() string {
	m.mu.Lock()
	srv := m.pac
	m.mu.Unlock()
	if srv == nil {
		return ""
	}
	return srv.Script()
}

// SetPACChanged задаёт обработчик изменения правил PAC. Вызывается вне
// блокировок менеджера, поэтому обработчик вправе обращаться к его методам.
func (m *Manager) SetPACChanged(fn func()) {
	m.mu.Lock()
	m.pacChanged = fn
	m.mu.Unlock()
}

// refreshPAC пересобирает правила по подключённым сейчас туннелям.
func (m *Manager) refreshPAC() {
	m.mu.Lock()
	srv := m.pac
	notify := m.pacChanged
	rules := make([]pac.Tunnel, 0, len(m.tunnels))
	for _, st := range m.tunnels {
		if st.pacRule != nil {
			rules = append(rules, *st.pacRule)
		}
	}
	m.mu.Unlock()

	if srv == nil {
		return
	}
	// Порядок карты случаен — фиксируем по имени, чтобы скрипт не «дрожал»
	// между обновлениями и его можно было сравнивать глазами.
	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	srv.SetTunnels(rules)

	// Набор туннелей в скрипте изменился — сообщаем подписчику, чтобы он
	// переприменил системный прокси: иначе система продолжит пользоваться
	// прежней (кэшированной) версией скрипта.
	if notify != nil {
		notify()
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
	opts := st.cfg.Opts
	if opts.Mode == "" {
		opts.Mode = st.cfg.Mode // режим туннеля задаётся конфигурацией менеджера
	}
	client := sslcon.NewClient(opts)
	if st.cfg.Mode == sslcon.ModeSOCKS5 {
		// В SOCKS5-режиме вместо TUN-адаптера поднимаем Bridge:
		// gVisor netstack поверх packet flow туннеля
		port := st.cfg.SocksPort
		client.TunnelSetup = func(t *sslcon.Tunnel) error {
			in, out := t.PacketFlow()
			bridge, err := socks5.NewBridge(port, in, out)
			if err != nil {
				return err
			}
			cSess := t.CSess()
			if addr := cSess.VPNAddress; addr != "" {
				if err := bridge.SetLocalAddress(addr); err != nil {
					_ = bridge.Close()
					return err
				}
			}
			// DNS-серверы шлюза: без них имена внутренней сети разрешались
			// бы системным (публичным) резолвером и не находились.
			bridge.SetDNS(socks5.DNSConfig{
				Servers:   cSess.DNS,
				Domains:   cSess.SplitDNS,
				TunnelAll: cSess.TunnelAllDNS,
			})
			if err := bridge.Start(ctx); err != nil {
				_ = bridge.Close()
				return err
			}
			m.mu.Lock()
			st.bridge = bridge
			st.pacRule = &pac.Tunnel{
				Name:      id,
				SocksPort: port,
				Domains:   cSess.SplitDNS,
				Subnets:   cSess.SplitInclude,
			}
			m.mu.Unlock()
			m.refreshPAC()
			m.emit(id, sslcon.Event{
				Type:    sslcon.EventConnected,
				Message: fmt.Sprintf("SOCKS5-прокси слушает на %s", bridge.Addr()),
			})
			return nil
		}
	}
	st.client = client
	m.mu.Unlock()

	if err := client.Connect(ctx); err != nil {
		m.mu.Lock()
		st.client = nil
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
	bridge := st.bridge
	st.bridge = nil
	st.pacRule = nil // браузер не должен ходить в остановленный туннель
	m.mu.Unlock()
	m.refreshPAC()

	if bridge != nil {
		_ = bridge.Close() // сначала останавливаем SOCKS5-мост, затем туннель
	}
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
	return client.Submit2FA(code)
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

// LookupResult — результат диагностического разрешения имени.
type LookupResult struct {
	IP      net.IP
	Source  string   // кто ответил: DNS внутри VPN или системный резолвер
	Servers []string // DNS-серверы, выданные шлюзом этого туннеля
}

// LookupIP разрешает имя через DNS указанного туннеля (режим socks5).
// Диагностический метод: показывает, работает ли разрешение имён внутри VPN
// отдельно от установления соединений.
func (m *Manager) LookupIP(ctx context.Context, id, name string) (LookupResult, error) {
	m.mu.Lock()
	st, ok := m.tunnels[id]
	var bridge *socks5.Bridge
	if ok {
		bridge = st.bridge
	}
	m.mu.Unlock()

	if !ok {
		return LookupResult{}, fmt.Errorf("туннель %q не найден", id)
	}
	if bridge == nil {
		return LookupResult{}, fmt.Errorf("у туннеля %q нет SOCKS5-моста (режим не socks5 или туннель не поднят)", id)
	}
	ip, source, err := bridge.LookupIP(ctx, name)
	return LookupResult{IP: ip, Source: source, Servers: bridge.DNSServers()}, err
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
	var bridge *socks5.Bridge
	if st, ok := m.tunnels[id]; ok && st.client == client {
		st.client = nil
		st.connected = false
		bridge = st.bridge
		st.bridge = nil
		st.pacRule = nil // туннель упал — снимаем его правила из PAC
	}
	m.mu.Unlock()
	m.refreshPAC()
	if bridge != nil {
		_ = bridge.Close() // туннель завершился — SOCKS5-мост больше не нужен
	}
}

// emit кладёт событие в агрегированный канал, не блокируясь при переполнении.
func (m *Manager) emit(id string, ev sslcon.Event) {
	select {
	case m.events <- ManagerEvent{TunnelID: id, Event: ev}:
	default: // потребитель отстал — событие статуса можно потерять
	}
}
