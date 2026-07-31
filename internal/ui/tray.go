// Иконка DualVPN в системном трее.
//
// Построена на github.com/getlantern/systray: на Windows — чистый Go
// (winapi), на Linux — cgo + ayatana-appindicator. Трей живёт в собственной
// горутине и общается с приложением только через методы App.
package ui

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/getlantern/systray"

	"dualvpn/internal/icons"
)

// Tray — обёртка над systray: контекстное меню с управлением туннелями
// и динамическими пунктами статуса (по одному на туннель из конфигурации).
type Tray struct {
	app    *App
	onShow func() // колбэк пункта «Показать окно»

	mu          sync.Mutex
	statusItems map[string]*systray.MenuItem // id туннеля → пункт статуса
	started     bool                         // systray.Run был запущен (см. Start/Stop)
}

// NewTray создаёт трей для приложения. onShow вызывается по пункту
// «Показать окно» (обычно это runtime.WindowShow).
func NewTray(app *App, onShow func()) *Tray {
	return &Tray{
		app:         app,
		onShow:      onShow,
		statusItems: make(map[string]*systray.MenuItem),
	}
}

// Start запускает цикл systray в отдельной горутине.
// systray.Run блокируется до systray.Quit, поэтому вызов неблокирующий.
//
// Если окружение не поддерживает системный трей (на Linux — нет session
// D-Bus), трей пропускается: getlantern/systray в этом случае аварийно
// завершает ПРОЦЕСС (SIGABRT на C-уровне внутри ayatana-appindicator, Go
// recover его не ловит), поэтому единственный надёжный путь — не запускать
// его вовсе. Окно приложения при этом продолжает работать без иконки в трее.
func (t *Tray) Start() {
	if !trayEnvAvailable() {
		log.Println("трей: окружение не поддерживает системный трей (нет session D-Bus) — пропускаю")
		return
	}
	t.started = true
	go systray.Run(t.onReady, t.onExit)
}

// Stop завершает цикл systray и убирает иконку из трея.
// Если трей не запускался (Start пропустил его), делать нечего.
func (t *Tray) Stop() {
	if !t.started {
		return
	}
	systray.Quit()
}

// trayEnvAvailable сообщает, можно ли поднимать системный трей в текущем
// окружении. На Windows/macOS трей не зависит от D-Bus — всегда true.
// На Linux нужен session D-Bus (иначе SIGABRT, см. Start).
func trayEnvAvailable() bool {
	if runtime.GOOS != "linux" {
		return true
	}
	return hasSessionBus()
}

// hasSessionBus эвристически определяет наличие session D-Bus: либо задан
// адрес шины DBUS_SESSION_BUS_ADDRESS, либо существует сокет
// $XDG_RUNTIME_DIR/bus (путь шины по умолчанию в современных сессиях).
func hasSessionBus() bool {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return true
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		if _, err := os.Stat(filepath.Join(xdg, "bus")); err == nil {
			return true
		}
	}
	return false
}

// UpdateStatus обновляет текст пункта статуса туннеля.
// Безопасно вызывать до готовности меню — неизвестные id игнорируются.
func (t *Tray) UpdateStatus(id string, connected bool) {
	t.mu.Lock()
	item, ok := t.statusItems[id]
	t.mu.Unlock()
	if !ok {
		return
	}
	item.SetTitle(statusText(id, connected))
}

// onReady вызывается systray после инициализации: ставит иконку и строит меню.
func (t *Tray) onReady() {
	systray.SetIcon(trayIcon())
	systray.SetTitle("DualVPN")
	systray.SetTooltip("DualVPN — два туннеля AnyConnect")

	header := systray.AddMenuItem("DualVPN", "")
	header.Disable()
	systray.AddSeparator()

	connectAll := systray.AddMenuItem("Подключить все", "Запустить все туннели")
	disconnectAll := systray.AddMenuItem("Отключить все", "Остановить все туннели")
	systray.AddSeparator()

	// Пункты статуса — некликабельные индикаторы, по одному на туннель.
	t.mu.Lock()
	for _, tun := range t.app.GetTunnels() {
		item := systray.AddMenuItem(statusText(tun.Name, false), "Статус туннеля")
		item.Disable()
		t.statusItems[tun.Name] = item
	}
	t.mu.Unlock()
	systray.AddSeparator()

	show := systray.AddMenuItem("Показать окно", "Открыть окно DualVPN")
	quit := systray.AddMenuItem("Выход", "Остановить туннели и выйти")

	go func() {
		for {
			select {
			case <-connectAll.ClickedCh:
				t.app.ConnectAll()
			case <-disconnectAll.ClickedCh:
				t.app.DisconnectAll()
			case <-show.ClickedCh:
				if t.onShow != nil {
					t.onShow()
				}
			case <-quit.ClickedCh:
				t.app.Quit() // останавливает туннели и закрывает Wails
				systray.Quit()
				return
			}
		}
	}()
}

// onExit вызывается systray при завершении цикла; ресурсов для очистки нет.
func (t *Tray) onExit() {}

// statusText — подпись пункта статуса туннеля.
func statusText(id string, connected bool) string {
	state := "отключен"
	if connected {
		state = "подключен"
	}
	return fmt.Sprintf("%s: %s", id, state)
}

// trayIcon возвращает байты иконки трея — общую эмблему приложения
// (та же, что у exe, ярлыков и установщика), встроенную в бинарь.
func trayIcon() []byte { return icons.Tray() }
