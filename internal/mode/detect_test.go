package mode

import (
	"os"
	"runtime"
	"testing"
)

// TestConstants фиксирует строковые значения режимов. Они не просто ярлыки:
// config.Validate (internal/config) сверяет mode.preferred с литералами
// "tun"/"socks5", а фронтенд шлёт их же в SwitchMode. Переименование значения
// здесь молча рассинхронит валидацию конфига и UI — тест ловит это.
func TestConstants(t *testing.T) {
	if TUN != "tun" {
		t.Errorf("TUN = %q, ожидалось \"tun\"", TUN)
	}
	if SOCKS5 != "socks5" {
		t.Errorf("SOCKS5 = %q, ожидалось \"socks5\"", SOCKS5)
	}
}

// TestDetectReturnsKnownMode: Detect всегда возвращает один из двух валидных
// режимов, что бы ни вернул IsAdmin.
func TestDetectReturnsKnownMode(t *testing.T) {
	got := Detect()
	if got != TUN && got != SOCKS5 {
		t.Fatalf("Detect() = %q, ожидался один из {%q, %q}", got, TUN, SOCKS5)
	}
}

// TestDetectConsistentWithIsAdmin: правило Detect() — админ⇒TUN, иначе SOCKS5.
// Проверяем связку на фактическом уровне прав тестового процесса (обычно
// не-root в CI → SOCKS5), не подменяя окружение.
func TestDetectConsistentWithIsAdmin(t *testing.T) {
	want := SOCKS5
	if IsAdmin() {
		want = TUN
	}
	if got := Detect(); got != want {
		t.Errorf("Detect() = %q, при IsAdmin()=%v ожидалось %q", got, IsAdmin(), want)
	}
}

// TestDetectDeterministic: без смены прав результат стабилен между вызовами —
// защита от случайной недетерминированности (например, состояния в глобале).
func TestDetectDeterministic(t *testing.T) {
	first := Detect()
	for i := 0; i < 5; i++ {
		if got := Detect(); got != first {
			t.Fatalf("Detect() недетерминерен: вызов %d вернул %q, первый — %q", i, got, first)
		}
	}
}

// TestIsAdminMatchesEuid: на Linux/macOS права администратора эквивалентны
// effective UID 0. Windows проверяет права иначе (доступ к \\.\PHYSICALDRIVE0),
// поэтому там сверку с euid не делаем.
func TestIsAdminMatchesEuid(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("на Windows права проверяются не через euid")
	}
	want := os.Geteuid() == 0
	if got := IsAdmin(); got != want {
		t.Errorf("IsAdmin() = %v, при euid=%d ожидалось %v", got, os.Geteuid(), want)
	}
}

// TestIsWindowsAdminDeniedWithoutDrive: проверка прав через открытие
// \\.\PHYSICALDRIVE0 при неудаче открытия обязана возвращать false. На
// не-Windows этого устройства нет, так что путь недоступен — удобно, чтобы
// зафиксировать безопасный дефолт: не смогли открыть ⇒ не админ. Обратное
// (ложный true) означало бы попытку поднять TUN без реальных прав.
func TestIsWindowsAdminDeniedWithoutDrive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("на Windows PHYSICALDRIVE0 реально открывается при повышенных правах")
	}
	if isWindowsAdmin() {
		t.Error("isWindowsAdmin() = true без доступного \\\\.\\PHYSICALDRIVE0, ожидалось false")
	}
}
