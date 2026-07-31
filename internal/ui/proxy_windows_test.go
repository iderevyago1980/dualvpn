//go:build windows

package ui

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"

	"dualvpn/internal/winproxy"
)

// settingsKey/autoConfigURL дублируют внутренние константы winproxy: тест
// проверяет фактическое состояние системных настроек, а не только код возврата.
const (
	settingsKey   = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	autoConfigURL = "AutoConfigURL"
)

// TestPACChangeReappliesProxy — главная проверка: после применения прокси
// подключение ещё одного туннеля меняет набор правил PAC, и системная
// настройка должна обновиться сама. Иначе система продолжает пользоваться
// прежней (кэшированной) версией скрипта, и трафик идёт только в тот
// туннель, который был поднят на момент нажатия кнопки.
func TestPACChangeReappliesProxy(t *testing.T) {
	restoreProxy(t)
	app := newTestApp(t)

	// PAC на свободном порту: без него применять нечего.
	if _, err := app.manager.EnablePAC(0); err != nil {
		t.Fatalf("EnablePAC: %v", err)
	}

	if err := app.ApplySystemProxy(); err != nil {
		t.Fatalf("ApplySystemProxy: %v", err)
	}
	first := readAutoConfigURL(t)
	if !strings.Contains(first, "/proxy.pac?v=") {
		t.Fatalf("в настройках прокси нет адреса PAC со счётчиком: %q", first)
	}

	// Имитируем изменение набора туннелей (в бою — подключение второго
	// туннеля: Manager.refreshPAC вызывает этот обработчик).
	app.onPACChanged()

	second := readAutoConfigURL(t)
	if second == first {
		t.Errorf("после изменения правил PAC адрес не обновился (%q) — система не перечитает скрипт", second)
	}
	if !strings.Contains(second, "/proxy.pac?v=") {
		t.Errorf("после обновления адрес PAC потерян: %q", second)
	}
}

// TestPACChangeIgnoredWhenProxyNotApplied — пока пользователь не нажал
// «Применить прокси», системные настройки трогать нельзя: подключение
// туннеля не должно менять чужую конфигурацию прокси.
func TestPACChangeIgnoredWhenProxyNotApplied(t *testing.T) {
	restoreProxy(t)
	app := newTestApp(t)
	if _, err := app.manager.EnablePAC(0); err != nil {
		t.Fatalf("EnablePAC: %v", err)
	}
	if err := winproxy.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	app.onPACChanged()

	if got := readAutoConfigURL(t); got != "" {
		t.Errorf("прокси применён без запроса пользователя: %q", got)
	}
}

// TestClearSystemProxyStopsReapplying — после снятия прокси изменения PAC
// не должны возвращать настройку обратно.
func TestClearSystemProxyStopsReapplying(t *testing.T) {
	restoreProxy(t)
	app := newTestApp(t)
	if _, err := app.manager.EnablePAC(0); err != nil {
		t.Fatalf("EnablePAC: %v", err)
	}
	if err := app.ApplySystemProxy(); err != nil {
		t.Fatalf("ApplySystemProxy: %v", err)
	}
	if err := app.ClearSystemProxy(); err != nil {
		t.Fatalf("ClearSystemProxy: %v", err)
	}

	app.onPACChanged()

	if got := readAutoConfigURL(t); got != "" {
		t.Errorf("снятый прокси вернулся при изменении PAC: %q", got)
	}
}

// restoreProxy запоминает исходную настройку прокси и восстанавливает её
// после теста — тесты не должны менять окружение разработчика.
func restoreProxy(t *testing.T) {
	t.Helper()
	orig := readAutoConfigURL(t)
	t.Cleanup(func() {
		var err error
		if orig != "" {
			err = winproxy.Apply(orig)
		} else {
			err = winproxy.Clear()
		}
		if err != nil {
			t.Logf("восстановление настроек прокси: %v", err)
		}
	})
}

// readAutoConfigURL возвращает текущий адрес PAC из системных настроек
// ("" — не задан).
func readAutoConfigURL(t *testing.T) string {
	t.Helper()
	k, err := registry.OpenKey(registry.CURRENT_USER, settingsKey, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("открытие настроек прокси: %v", err)
	}
	defer k.Close() //nolint:errcheck // ключ открыт только для чтения
	v, _, err := k.GetStringValue(autoConfigURL)
	if errors.Is(err, registry.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatalf("чтение %s: %v", autoConfigURL, err)
	}
	return v
}
