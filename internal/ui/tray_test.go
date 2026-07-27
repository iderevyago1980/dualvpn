package ui

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHasSessionBus — определение доступности session D-Bus, от которой
// зависит, можно ли поднимать системный трей на Linux (без неё
// getlantern/systray аварийно завершает процесс на C-уровне).
func TestHasSessionBus(t *testing.T) {
	// Явно задан адрес шины → доступна.
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if !hasSessionBus() {
		t.Error("при заданном DBUS_SESSION_BUS_ADDRESS ожидался true")
	}

	// Нет адреса и нет сокета $XDG_RUNTIME_DIR/bus → недоступна.
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if hasSessionBus() {
		t.Error("без адреса шины и без сокета ожидался false")
	}

	// Нет адреса, но есть сокет $XDG_RUNTIME_DIR/bus → доступна.
	dir := t.TempDir()
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("XDG_RUNTIME_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "bus"), nil, 0o600); err != nil {
		t.Fatalf("создание сокета bus: %v", err)
	}
	if !hasSessionBus() {
		t.Error("при наличии сокета $XDG_RUNTIME_DIR/bus ожидался true")
	}
}
