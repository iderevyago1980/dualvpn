//go:build !windows

package winproxy

import "errors"

// errUnsupported — на не-Windows платформах системный прокси через WinINET
// неприменим (в Linux/macOS настройка прокси устроена иначе и здесь не нужна:
// целевая платформа кнопки «Применить прокси» — Windows).
var errUnsupported = errors.New("winproxy: системный прокси поддерживается только на Windows")

// Apply на не-Windows платформах возвращает ошибку неподдерживаемой операции.
func Apply(_ string) error { return errUnsupported }

// Clear на не-Windows платформах возвращает ошибку неподдерживаемой операции.
func Clear() error { return errUnsupported }
