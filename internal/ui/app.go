// Package ui — слой Wails-биндингов DualVPN.
//
// Экспортируемые методы структуры App доступны фронтенду через
// window.go.ui.App.<Метод>, события реального времени — через
// runtime.EventsEmit → window.runtime.EventsOn.
package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"dualvpn/internal/config"
	"dualvpn/internal/mode"
	"dualvpn/internal/vpn"
	"dualvpn/internal/vpn/sslcon"
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

	mu       sync.Mutex
	cfg      *config.Config
	mode     string // итоговый режим работы: "tun" | "socks5"
	logBuf   []LogEntry
	tray     *Tray
	quitting bool // true после команды «Выход» — разрешает закрытие окна
}

// NewApp загружает конфигурацию (создавая файл по умолчанию при отсутствии),
// определяет режим работы и регистрирует туннели в менеджере.
// NewApp создаёт приложение. starterConfig — встроенный шаблон
// config.example.toml: разворачивается в cfgPath при первом запуске,
// когда файла конфигурации ещё нет. Пустой шаблон допустим (тесты,
// сборки без встраивания) — тогда создаётся конфиг по умолчанию.
func NewApp(cfgPath string, starterConfig []byte) (*App, error) {
	cfg, err := loadOrCreate(cfgPath, starterConfig)
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

// Startup вызывается Wails при старте приложения: сохраняет контекст,
// запускает системный трей и трансляцию событий менеджера во фронтенд.
func (a *App) Startup(ctx context.Context) {
	tray := NewTray(a, a.ShowWindow)

	a.mu.Lock()
	a.ctx = ctx
	a.tray = tray
	a.mu.Unlock()

	tray.Start()
	go a.forwardEvents()
	a.startPAC()
	a.log("info", fmt.Sprintf("DualVPN запущен: режим %s, туннелей %d, admin=%v",
		a.GetMode(), len(a.GetTunnels()), mode.IsAdmin()))
}

// Shutdown вызывается Wails при выходе: останавливает трей и все туннели.
func (a *App) Shutdown(_ context.Context) {
	a.mu.Lock()
	tray := a.tray
	a.mu.Unlock()

	if tray != nil {
		tray.Stop()
	}
	a.manager.StopAll()
}

// BeforeClose перехватывает закрытие окна: вместо выхода прячет его в трей.
// Возвращает true (отменить закрытие), пока «Выход» не выбран в трее.
func (a *App) BeforeClose(ctx context.Context) bool {
	a.mu.Lock()
	quitting := a.quitting
	a.mu.Unlock()

	if quitting {
		return false
	}
	runtime.WindowHide(ctx)
	a.log("info", "окно скрыто в системный трей")
	return true
}

// Quit — выход по команде из трея: останавливает туннели и закрывает
// Wails-приложение (окно к этому моменту может быть скрыто).
func (a *App) Quit() {
	a.mu.Lock()
	a.quitting = true
	ctx := a.ctx
	a.mu.Unlock()

	a.manager.StopAll()
	if ctx != nil {
		runtime.Quit(ctx)
	}
}

// ShowWindow показывает главное окно (вызов из трея или фронтенда).
func (a *App) ShowWindow() {
	if ctx := a.context(); ctx != nil {
		runtime.WindowShow(ctx)
	}
}

// HideWindow прячет главное окно в трей.
func (a *App) HideWindow() {
	if ctx := a.context(); ctx != nil {
		runtime.WindowHide(ctx)
	}
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
	a.log("info", fmt.Sprintf("туннель %q: подключение", id))
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

// version — версия сборки. Подставляется линкером:
//
//	-ldflags "-X dualvpn/internal/ui.version=1.8.0"
//
// Значение по умолчанию помечает сборку, собранную без указания версии
// (go build без Makefile), чтобы в интерфейсе не появлялось выдуманное число.
var version = "dev"

// Version возвращает версию сборки для отображения в интерфейсе.
func (a *App) Version() string { return version }

// startPAC поднимает раздачу PAC-файла для браузера. Имеет смысл только в
// режиме SOCKS5: в TUN-режиме трафик и так идёт по маршрутам системы.
// Занятый порт не считается ошибкой запуска приложения — берём свободный
// и сообщаем фактический адрес.
func (a *App) startPAC() {
	if a.GetMode() != mode.SOCKS5 {
		return
	}
	port := a.GetConfig().PACPort()
	url, err := a.manager.EnablePAC(port)
	if err != nil {
		a.log("warn", fmt.Sprintf("порт %d для PAC занят (%v) — беру свободный", port, err))
		if url, err = a.manager.EnablePAC(0); err != nil {
			a.log("err", "автонастройка прокси недоступна: "+err.Error())
			return
		}
	}
	a.log("ok", "автонастройка прокси для браузера: "+url)
}

// PACURL возвращает адрес PAC-файла для настроек браузера
// ("" — раздача не запущена, например в TUN-режиме).
func (a *App) PACURL() string { return a.manager.PACURL() }

// PACScript возвращает текущий текст PAC-скрипта (диагностика в UI).
func (a *App) PACScript() string { return a.manager.PACScript() }

// FetchGroups запрашивает у сервера список групп (алиасов tunnel-group).
// Имя группы должно совпадать с одним из них буквально, поэтому список
// берётся с сервера, а не задаётся в приложении: набор групп меняется на
// стороне VPN-шлюза и зашитый в код перечень неизбежно устаревает.
func (a *App) FetchGroups(endpoint string) ([]string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, errors.New("не указан адрес VPN-сервера")
	}
	groups, err := sslcon.FetchGroups(endpoint, false)
	if err != nil {
		a.log("err", fmt.Sprintf("список групп %s: %v", endpoint, err))
		return nil, err
	}
	if len(groups) == 0 {
		a.log("warn", fmt.Sprintf("сервер %s не предлагает выбор группы", endpoint))
		return nil, nil
	}
	a.log("ok", fmt.Sprintf("группы %s: %s", endpoint, strings.Join(groups, ", ")))
	return groups, nil
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
			Opts: sslcon.ClientConfig{
				Host:     t.Endpoint,
				Group:    t.Group,
				Username: t.Username,
				Password: t.Password,
				TunName:  t.TunName,
			},
			Routes:    t.Routes,
			Mode:      m,
			SocksPort: t.SocksPort,
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
		case sslcon.EventConnected:
			level = "ok"
		case sslcon.EventDisconnected, sslcon.Event2FARequired:
			level = "warn"
		case sslcon.EventError:
			level = "err"
		}
		a.log(level, fmt.Sprintf("[%s] %s: %s", ev.TunnelID, ev.Event.Type, ev.Event.Message))

		// Отражаем смену состояния туннеля в меню трея.
		switch ev.Event.Type {
		case sslcon.EventConnected:
			a.trayStatus(ev.TunnelID, true)
		case sslcon.EventDisconnected, sslcon.EventError:
			a.trayStatus(ev.TunnelID, false)
		}

		if ctx := a.context(); ctx != nil {
			runtime.EventsEmit(ctx, "tunnel:event", EventPayload{
				TunnelID: ev.TunnelID,
				Type:     string(ev.Event.Type),
				Message:  ev.Event.Message,
			})
			if ev.Event.Type == sslcon.Event2FARequired {
				runtime.EventsEmit(ctx, "tunnel:2fa", ev.TunnelID)
			}
		}
	}
}

// trayStatus обновляет пункт статуса туннеля в трее (если трей запущен).
func (a *App) trayStatus(id string, connected bool) {
	a.mu.Lock()
	tray := a.tray
	a.mu.Unlock()

	if tray != nil {
		tray.UpdateStatus(id, connected)
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

// loadOrCreate загружает конфиг; если файла нет — разворачивает встроенный
// шаблон config.example.toml (вместе с комментариями).
func loadOrCreate(path string, starter []byte) (*config.Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return config.CreateFrom(path, starter)
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
