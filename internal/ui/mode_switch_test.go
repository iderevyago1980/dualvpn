package ui

import (
	"strings"
	"testing"

	"dualvpn/internal/mode"
)

// TestSwitchToSocks5StartsPAC — при переходе в SOCKS5 раздача PAC должна
// подняться. Раньше PAC запускался только при старте приложения, и админ,
// стартовавший в TUN, после переключения оставался без PAC: браузеру
// некуда было смотреть, а кнопка «Применить прокси» не появлялась.
func TestSwitchToSocks5StartsPAC(t *testing.T) {
	app := newTestApp(t)

	// Гарантируем исходное состояние без раздачи.
	if err := app.manager.DisablePAC(); err != nil {
		t.Fatalf("DisablePAC: %v", err)
	}
	if url := app.PACURL(); url != "" {
		t.Fatalf("перед проверкой PAC уже запущен: %q", url)
	}

	if err := app.SwitchMode(mode.SOCKS5); err != nil {
		t.Fatalf("SwitchMode(socks5): %v", err)
	}
	if url := app.PACURL(); url == "" {
		t.Error("после перехода в SOCKS5 раздача PAC не запущена")
	}
}

// TestNeedsAdminMessageMatchesError — фронтенд отличает отказ «нужны права
// администратора» от прочих сбоев сравнением с этим текстом, поэтому он
// обязан совпадать с текстом ошибки SwitchMode.
func TestNeedsAdminMessageMatchesError(t *testing.T) {
	if mode.IsAdmin() {
		t.Skip("тест имеет смысл только без прав администратора")
	}
	app := newTestApp(t)
	err := app.SwitchMode(mode.TUN)
	if err == nil {
		t.Fatal("без прав администратора SwitchMode(tun) должен вернуть ошибку")
	}
	if msg := app.NeedsAdminMessage(); !strings.Contains(err.Error(), msg) {
		t.Errorf("текст ошибки %q не содержит NeedsAdminMessage() = %q", err.Error(), msg)
	}
}

// TestRestartAsAdminRejectedUnderAdmin — под администратором перезапускаться
// незачем: повторный UAC-диалог пользователю ничего не даёт.
func TestRestartAsAdminRejectedUnderAdmin(t *testing.T) {
	if !mode.IsAdmin() {
		t.Skip("тест имеет смысл только с правами администратора")
	}
	app := newTestApp(t)
	if err := app.RestartAsAdmin(); err == nil {
		t.Error("под администратором RestartAsAdmin должен вернуть ошибку, а не перезапускать приложение")
	}
}
