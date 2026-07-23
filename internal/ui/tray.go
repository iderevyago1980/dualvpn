// Иконка DualVPN в системном трее.
//
// Построена на github.com/getlantern/systray: на Windows — чистый Go
// (winapi), на Linux — cgo + ayatana-appindicator. Трей живёт в собственной
// горутине и общается с приложением только через методы App.
package ui

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"runtime"
	"sync"

	"github.com/getlantern/systray"
)

// Tray — обёртка над systray: контекстное меню с управлением туннелями
// и динамическими пунктами статуса (по одному на туннель из конфигурации).
type Tray struct {
	app    *App
	onShow func() // колбэк пункта «Показать окно»

	mu          sync.Mutex
	statusItems map[string]*systray.MenuItem // id туннеля → пункт статуса
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
func (t *Tray) Start() {
	go systray.Run(t.onReady, t.onExit)
}

// Stop завершает цикл systray и убирает иконку из трея.
func (t *Tray) Stop() {
	systray.Quit()
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

// trayIcon возвращает байты иконки трея — синий круг 16x16.
// systray на Windows принимает только формат ICO, поэтому там PNG
// оборачивается в ICO-контейнер (PNG внутри ICO поддерживается с Vista).
func trayIcon() []byte {
	data := circlePNG()
	if runtime.GOOS == "windows" {
		return pngToICO(data)
	}
	return data
}

// circlePNG рисует синий круг 16x16 и кодирует его в PNG в памяти.
func circlePNG() []byte {
	const size = 16
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	blue := color.RGBA{R: 0x1e, G: 0x66, B: 0xd0, A: 0xff}
	c := float64(size-1) / 2
	r := float64(size)/2 - 1
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)-c, float64(y)-c
			if dx*dx+dy*dy <= r*r {
				img.SetRGBA(x, y, blue)
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img) // запись в bytes.Buffer не возвращает ошибок
	return buf.Bytes()
}

// pngToICO оборачивает PNG в минимальный ICO-контейнер с одной иконкой.
func pngToICO(pngData []byte) []byte {
	var buf bytes.Buffer
	le := binary.LittleEndian
	// ICONDIR: reserved, тип (1 = icon), число иконок.
	_ = binary.Write(&buf, le, uint16(0))
	_ = binary.Write(&buf, le, uint16(1))
	_ = binary.Write(&buf, le, uint16(1))
	// ICONDIRENTRY: 16x16, без палитры, 1 plane, 32 bpp, размер и смещение данных.
	buf.Write([]byte{16, 16, 0, 0})
	_ = binary.Write(&buf, le, uint16(1))
	_ = binary.Write(&buf, le, uint16(32))
	_ = binary.Write(&buf, le, uint32(len(pngData)))
	_ = binary.Write(&buf, le, uint32(6+16)) // заголовок + одна запись каталога
	buf.Write(pngData)
	return buf.Bytes()
}
