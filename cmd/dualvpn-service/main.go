// Package main (dualvpn-service) — служба Windows, которая держит
// TUN-туннели DualVPN.
//
// Создание Wintun-адаптера, маршруты, DNS и правила NRPT требуют прав
// администратора, а обмен пакетами привязан к процессу, создавшему адаптер.
// Поэтому туннели живут здесь, под LocalSystem, а графическое приложение
// запускается обычным пользователем и управляет службой через именованный
// канал (internal/ipc). Права администратора нужны один раз — при установке.
//
// Команды:
//
//	dualvpn-service install    зарегистрировать и запустить службу
//	dualvpn-service uninstall  остановить и удалить службу
//	dualvpn-service run        выполнить в консоли (отладка, нужен админ)
//
// Без аргументов запускается как служба (так её вызывает Windows).
package main

import (
	"fmt"
	"os"
)

// serviceName — имя службы в диспетчере управления службами.
const serviceName = "DualVPN"

// serviceDisplay — отображаемое имя и описание в оснастке «Службы».
const (
	serviceDisplay = "DualVPN"
	serviceDesc    = "Туннели DualVPN (Cisco AnyConnect). Позволяет пользователям " +
		"поднимать VPN в режиме TUN без прав администратора."
)

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	if err := dispatch(cmd); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}
