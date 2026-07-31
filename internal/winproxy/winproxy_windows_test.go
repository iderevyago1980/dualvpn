//go:build windows

package winproxy

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// TestApplyClearRoundTrip проверяет, что Apply прописывает AutoConfigURL, а
// Clear его убирает. Тест восстанавливает исходное значение, чтобы не менять
// реальные настройки прокси пользователя.
func TestApplyClearRoundTrip(t *testing.T) {
	// Сохраняем исходное значение (если было) и гарантируем восстановление.
	orig, hadOrig := readAutoConfigURL(t)
	t.Cleanup(func() {
		if hadOrig {
			if err := Apply(orig); err != nil {
				t.Logf("восстановление исходного AutoConfigURL: %v", err)
			}
		} else {
			if err := Clear(); err != nil {
				t.Logf("сброс AutoConfigURL: %v", err)
			}
		}
	})

	const want = "http://127.0.0.1:65000/proxy.pac"
	if err := Apply(want); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, ok := readAutoConfigURL(t)
	if !ok || got != want {
		t.Fatalf("после Apply AutoConfigURL = %q (present=%v), ожидалось %q", got, ok, want)
	}

	if err := Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, ok := readAutoConfigURL(t); ok {
		t.Fatalf("после Clear AutoConfigURL всё ещё задан")
	}

	// Повторный Clear на отсутствующем значении не должен быть ошибкой.
	if err := Clear(); err != nil {
		t.Fatalf("повторный Clear: %v", err)
	}
}

// TestApplyEmptyRejected — пустой URL применять нельзя.
func TestApplyEmptyRejected(t *testing.T) {
	if err := Apply(""); err == nil {
		t.Fatal("Apply(\"\") должен вернуть ошибку")
	}
}

// readAutoConfigURL читает текущее значение AutoConfigURL из HKCU.
func readAutoConfigURL(t *testing.T) (string, bool) {
	t.Helper()
	k, err := registry.OpenKey(registry.CURRENT_USER, settingsKey, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("открытие настроек прокси: %v", err)
	}
	defer k.Close()
	v, _, err := k.GetStringValue(autoConfigURL)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatalf("чтение AutoConfigURL: %v", err)
	}
	return v, true
}
