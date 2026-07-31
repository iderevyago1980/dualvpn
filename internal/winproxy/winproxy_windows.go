//go:build windows

// Package winproxy — управление per-user настройками прокси Windows (WinINET).
//
// В режиме SOCKS5 приложение раздаёт PAC-скрипт, но браузеры и прочие
// программы не используют его, пока он не прописан в системных настройках
// прокси. Apply прописывает адрес PAC в AutoConfigURL текущего пользователя
// (раздел реестра HKCU — прав администратора не требует) и уведомляет
// WinINET, после чего Edge/Chrome и другие WinINET-приложения начинают сами
// выбирать туннель по домену. Clear возвращает всё к прямому соединению.
package winproxy

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// settingsKey — раздел per-user настроек интернета, где WinINET хранит
// параметры прокси (в т.ч. AutoConfigURL — адрес PAC-скрипта).
const settingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// autoConfigURL — имя значения реестра с адресом PAC-скрипта.
const autoConfigURL = "AutoConfigURL"

var (
	wininet               = windows.NewLazySystemDLL("wininet.dll")
	procInternetSetOption = wininet.NewProc("InternetSetOptionW")
)

// Коды опций InternetSetOption для оповещения об изменении настроек.
const (
	internetOptionSettingsChanged = 39 // INTERNET_OPTION_SETTINGS_CHANGED
	internetOptionRefresh         = 37 // INTERNET_OPTION_REFRESH
)

// Apply прописывает PAC-URL в AutoConfigURL текущего пользователя и уведомляет
// систему. Прав администратора не требует. Ошибка означает, что настройка не
// изменена.
func Apply(pacURL string) error {
	if pacURL == "" {
		return errors.New("winproxy: пустой PAC-URL")
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, settingsKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("winproxy: открытие настроек прокси: %w", err)
	}
	defer k.Close() //nolint:errcheck // ключ открыт только для записи значения
	if err := k.SetStringValue(autoConfigURL, pacURL); err != nil {
		return fmt.Errorf("winproxy: запись %s: %w", autoConfigURL, err)
	}
	notifyWinINET()
	return nil
}

// Clear убирает AutoConfigURL (возврат к прямому соединению) и уведомляет
// систему. Отсутствие значения ошибкой не считается — снимать нечего.
func Clear() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, settingsKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("winproxy: открытие настроек прокси: %w", err)
	}
	defer k.Close() //nolint:errcheck // ключ открыт только для удаления значения
	if err := k.DeleteValue(autoConfigURL); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("winproxy: удаление %s: %w", autoConfigURL, err)
	}
	notifyWinINET()
	return nil
}

// notifyWinINET сообщает подсистеме WinINET, что настройки прокси изменились,
// чтобы уже запущенные приложения (Edge, Chrome и т.п.) подхватили их без
// перезапуска.
func notifyWinINET() {
	// Ошибки вызова игнорируем: сами настройки уже записаны в реестр и
	// вступят в силу при следующем чтении, даже если оповещение не прошло.
	_, _, _ = procInternetSetOption.Call(0, internetOptionSettingsChanged, 0, 0)
	_, _, _ = procInternetSetOption.Call(0, internetOptionRefresh, 0, 0)
}
