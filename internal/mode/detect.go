// Package mode — автоопределение режима работы DualVPN.
//
// Режим "tun" требует админ-прав (создание TUN-интерфейса и маршрутов),
// режим "socks5" работает от обычного пользователя (openconnect + ocproxy).
package mode

import (
	"os"
	"runtime"
)

// Режимы работы приложения.
const (
	TUN    = "tun"    // Полноценный TUN-интерфейс через vpnc-script (нужен админ)
	SOCKS5 = "socks5" // SOCKS5-прокси через ocproxy (админ не нужен)
)

// Detect определяет доступный режим: при наличии админ-прав — "tun",
// иначе — "socks5".
func Detect() string {
	if IsAdmin() {
		return TUN
	}
	return SOCKS5
}

// IsAdmin сообщает, запущен ли процесс с правами администратора.
func IsAdmin() bool {
	switch runtime.GOOS {
	case "windows":
		return isWindowsAdmin()
	default:
		// Linux/macOS: root имеет effective UID 0.
		return os.Geteuid() == 0
	}
}

// isWindowsAdmin проверяет права администратора на Windows.
// Открытие физического диска разрешено только процессам с повышенными
// правами — это стандартный способ проверки без вызова WinAPI.
func isWindowsAdmin() bool {
	f, err := os.Open(`\\.\PHYSICALDRIVE0`)
	if err != nil {
		return false
	}
	f.Close() //nolint:errcheck // дескриптор открывался только для проверки прав
	return true
}
