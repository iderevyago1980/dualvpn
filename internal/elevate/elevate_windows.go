//go:build windows

// Package elevate — перезапуск приложения с правами администратора.
//
// Режим TUN требует прав администратора, а per-user установка запускается
// без них. Повысить права у уже запущенного процесса Windows не позволяет:
// единственный путь — запустить новый экземпляр через ShellExecute с
// глаголом "runas", на что система спрашивает подтверждение (UAC).
package elevate

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// swShowNormal — обычное окно для запускаемого процесса (SW_SHOWNORMAL).
const swShowNormal = 1

// Relaunch запускает новый экземпляр приложения с запросом прав
// администратора и возвращается сразу после запуска: завершение текущего
// процесса — забота вызывающего. Отказ пользователя в диалоге UAC
// возвращается ошибкой.
func Relaunch() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("elevate: путь к исполняемому файлу: %w", err)
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return fmt.Errorf("elevate: подготовка команды: %w", err)
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return fmt.Errorf("elevate: подготовка пути %q: %w", exe, err)
	}

	// Аргументы командной строки сохраняем: иначе перезапуск потерял бы,
	// например, путь к конфигурации, заданный при запуске.
	var args *uint16
	if rest := os.Args[1:]; len(rest) > 0 {
		if args, err = windows.UTF16PtrFromString(strings.Join(rest, " ")); err != nil {
			return fmt.Errorf("elevate: подготовка аргументов: %w", err)
		}
	}

	if err := windows.ShellExecute(0, verb, file, args, nil, swShowNormal); err != nil {
		if err == windows.ERROR_CANCELLED {
			return fmt.Errorf("elevate: запуск от администратора отменён")
		}
		return fmt.Errorf("elevate: запуск от администратора: %w", err)
	}
	return nil
}
