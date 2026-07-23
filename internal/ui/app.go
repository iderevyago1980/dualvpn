// Package ui — слой Wails-биндингов DualVPN.
//
// Экспортируемые методы структуры App доступны фронтенду через
// window.go.ui.App.<Метод>, события реального времени — через
// runtime.EventsEmit → window.runtime.EventsOn.
package ui

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"dualvpn/internal/config"
	"dualvpn/internal/mode"
	"dualvpn/internal/vpn"
)

// LogEntry — одна запись журнала приложения; отдаётся фронтенду как есть.
type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"` // info | ok | warn | err
	Message string    `json:"message"`
}

// TunnelStatus — статус туннеля для фронтенда.
// Отдельная структура вместо пары значений: Wails v2 допускает максимум
// два возвращаемых значения, и второе обязано быть error.
type TunnelStatus struct {
	Connected bool   `json:"connected"`
	Mode      string `json:"mode"`
}

// EventPayload — сериализуемое событие туннеля для события "tunnel:event".
type EventPayload struct {
	TunnelID string `json:"tunnelId"`
	Type     string `json:"type"`
	Message  string `json:"message"`
}

// maxLogEntries — ограничение кольцевого буфера журнала.
const maxLogEntries = 1000

// App — контекст Wails-приложения: держит менеджер туннелей, конфигурацию
// и буфер журнала. Все экспортируемые методы — биндинги для JS.
type App struct {
	ctx     context.Context
	manager *vpn.Manager
	cfgPath string

	mu     sync.Mutex
	cfg    *config.Config
	mode   string // итоговый режим работы: "tun" | "socks5"
	logBuf []LogEntry
}

// NewApp загружает конфигурацию (создавая файл по умолчанию при отсутствии),
// определяет режим работы и регистрирует туннели в менеджере.
func NewApp(cfgPath string) (*App, error) {
	cfg, err := loadOrCreate(cfgPath)
	if err != nil {
		return nil, err
	}
	a := &App{
		manager: vpn.NewManager(),
		cfgPath: cfgPath,
		cfg:     cfg,
		mode:    resolveMode(cfg.Mode.Preferred),
	}
	a.registerTunnels()
	return a, nil
}

// Startup вызывается Wails при старте приложения: сохраняет контекст
// и запускает трансляцию событий менеджера во фронтенд.
func (a *App) Startup(ctx context.Context) {
	a.mu.Lock()
	a.ctx = ctx
	a.mu.Unlock()

	go a.forwardEvents()
	a.log("info", fmt.Sprintf("DualVPN запущен: режим %s, туннелей %d, admin=%v",
		a.GetMode(), len(a.GetTunnels()), mode.IsAdmin()))
}

// Shutdown вызывается Wails при закрытии окна: останавливает все туннели.
func (a *App) Shutdown(_ context.Context) {
	a.manager.StopAll()
}

// GetTunnels возвращает список туннелей из конфигурации.
func (a *App) GetTunnels() []config.Tunnel {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]config.Tunnel(nil), a.cfg.Tunnels...)
}

// GetMode возвращает текущий (уже разрешённый) режим работы: tun | socks5.
func (a *App) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

// DetectMode выполняет автодетекцию доступного режима по правам процесса.
func (a *App) DetectMode() string {
	return mode.Detect()
}

// SwitchMode переключает режим работы (auto|tun|socks5).
// Все активные туннели останавливаются: сменить режим «на лету» нельзя.
func (a *App) SwitchMode(m string) error {
	switch m {
	case "auto":
		m = mode.Detect()
	case mode.TUN, mode.SOCKS5:
	default:
		return fmt.Errorf("неизвестный режим %q (ожидается auto|tun|socks5)", m)
	}
	if m == mode.TUN && !mode.IsAdmin() {
		return fmt.Errorf("режим tun требует прав администратора — перезапустите с sudo или используйте socks5")
	}

	a.mu.Lock()
	a.mode = m
	a.mu.Unlock()

	a.registerTunnels() // остановит активные туннели и перерегистрирует с новым режимом
	a.log("info", "режим переключён: "+m)
	return nil
}

// ConnectTunnel запускает один туннель по идентификатору (имени).
func (a *App) ConnectTunnel(id string) error {
	if err := a.manager.Start(a.context(), id); err != nil {
		a.log("err", err.Error())
		return err
	}
	a.log("info", fmt.Sprintf("туннель %q: запуск openconnect", id))
	return nil
}

// DisconnectTunnel останавливает один туннель по идентификатору.
func (a *App) DisconnectTunnel(id string) error {
	if err := a.manager.Stop(id); err != nil {
		a.log("err", err.Error())
		return err
	}
	a.log("info", fmt.Sprintf("туннель %q: остановка", id))
	return nil
}

// ConnectAll запускает все зарегистрированные туннели.
// Ошибки отдельных туннелей приходят событиями "tunnel:event" (type=error).
func (a *App) ConnectAll() {
	a.log("info", "запуск всех туннелей")
	a.manager.StartAll(a.context())
}

// DisconnectAll останавливает все запущенные туннели.
func (a *App) DisconnectAll() {
	a.log("info", "остановка всех туннелей")
	a.manager.StopAll()
}

// Submit2FA передаёт 2FA-код (TOTP) туннелю, запросившему второй фактор.
func (a *App) Submit2FA(tunnelID, code string) error {
	if err := a.manager.Submit2FA(tunnelID, code); err != nil {
		a.log("err", err.Error())
		return err
	}
	a.log("info", fmt.Sprintf("туннель %q: 2FA-код отправлен", tunnelID))
	return nil
}

// GetTunnelStatus возвращает статус туннеля: подключён ли и режим работы.
func (a *App) GetTunnelStatus(id string) TunnelStatus {
	connected, m := a.manager.Status(id)
	return TunnelStatus{Connected: connected, Mode: m}
}

// GetLogs возвращает накопленный журнал приложения.
func (a *App) GetLogs() []LogEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]LogEntry(nil), a.logBuf...)
}

// SaveConfig валидирует и сохраняет конфигурацию, затем перерегистрирует
// туннели. Активные подключения при этом останавливаются.
func (a *App) SaveConfig(cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		a.log("err", "конфигурация отклонена: "+err.Error())
		return err
	}
	if err := cfg.Save(a.cfgPath); err != nil {
		a.log("err", err.Error())
		return err
	}

	a.mu.Lock()
	a.cfg = &cfg
	a.mu.Unlock()

	a.registerTunnels()
	a.log("ok", "конфигурация сохранена: "+a.cfgPath)
	return nil
}

// GetConfig возвращает текущую конфигурацию целиком.
func (a *App) GetConfig() *config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := *a.cfg
	cp.Tunnels = append([]config.Tunnel(nil), a.cfg.Tunnels...)
	return &cp
}

// IsAdmin сообщает, запущено ли приложение с правами администратора.
func (a *App) IsAdmin() bool {
	return mode.IsAdmin()
}

// context возвращает Wails-контекст приложения (nil до Startup).
func (a *App) context() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ctx
}

// registerTunnels заменяет набор туннелей менеджера туннелями из конфига
// с текущим режимом работы. Активные подключения останавливаются.
func (a *App) registerTunnels() {
	a.mu.Lock()
	tunnels := append([]config.Tunnel(nil), a.cfg.Tunnels...)
	m := a.mode
	a.mu.Unlock()

	cfgs := make([]vpn.TunnelConfig, 0, len(tunnels))
	for _, t := range tunnels {
		cfgs = append(cfgs, vpn.TunnelConfig{
			ID: t.Name,
			Opts: vpn.Options{
				Server:    t.Endpoint,
				Group:     t.Group,
				Username:  t.Username,
				Password:  t.Password,
				Mode:      m,
				SocksPort: t.SocksPort,
			},
			Routes: t.Routes,
		})
	}
	a.manager.ReplaceTunnels(cfgs)
}

// forwardEvents читает агрегированный канал менеджера и транслирует события
// во фронтенд: "tunnel:event" — всегда, "tunnel:2fa" — при запросе кода.
func (a *App) forwardEvents() {
	for ev := range a.manager.Events() {
		level := "info"
		switch ev.Event.Type {
		case vpn.EventConnected:
			level = "ok"
		case vpn.EventDisconnected, vpn.Event2FARequired:
			level = "warn"
		case vpn.EventError:
			level = "err"
		}
		a.log(level, fmt.Sprintf("[%s] %s: %s", ev.TunnelID, ev.Event.Type, ev.Event.Message))

		if ctx := a.context(); ctx != nil {
			runtime.EventsEmit(ctx, "tunnel:event", EventPayload{
				TunnelID: ev.TunnelID,
				Type:     string(ev.Event.Type),
				Message:  ev.Event.Message,
			})
			if ev.Event.Type == vpn.Event2FARequired {
				runtime.EventsEmit(ctx, "tunnel:2fa", ev.TunnelID)
			}
		}
	}
}

// log добавляет запись в буфер журнала и шлёт событие "log" во фронтенд.
func (a *App) log(level, msg string) {
	entry := LogEntry{Time: time.Now(), Level: level, Message: msg}

	a.mu.Lock()
	a.logBuf = append(a.logBuf, entry)
	if len(a.logBuf) > maxLogEntries {
		a.logBuf = a.logBuf[len(a.logBuf)-maxLogEntries:]
	}
	ctx := a.ctx
	a.mu.Unlock()

	if ctx != nil {
		runtime.EventsEmit(ctx, "log", entry)
	}
}

// loadOrCreate загружает конфиг; если файла нет — создаёт из значений по умолчанию.
func loadOrCreate(path string) (*config.Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		cfg := config.Default()
		if err := cfg.Save(path); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	return config.Load(path)
}

// resolveMode разрешает значение из конфига: "auto" (или пустое) —
// автодетекцией админ-прав, иначе используется как есть.
func resolveMode(preferred string) string {
	if preferred == "auto" || preferred == "" {
		return mode.Detect()
	}
	return preferred
}
