// Package main (dualvpn-tuncheck) — самопроверка TUN-пути без VPN-сервера.
//
// Создаёт TUN-адаптер (Windows: Wintun, Linux: /dev/net/tun), назначает
// адрес и MTU, добавляет и снимает тестовый маршрут через internal/routing —
// то есть ровно те системные вызовы, которые делает боевой TUN-режим после
// установления туннеля. Позволяет отделить проблемы драйвера/прав/маршрутов
// от проблем аутентификации.
//
// Требует прав администратора (root). Windows: рядом с exe нужен wintun.dll —
// драйвер грузится только из каталога программы и System32.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"dualvpn/internal/mode"
	"dualvpn/internal/routing"
	"dualvpn/internal/tun"
)

func main() {
	var (
		name  = flag.String("name", "dualvpn-check", "имя создаваемого интерфейса")
		addr  = flag.String("addr", "10.99.99.2", "IPv4-адрес интерфейса")
		mtu   = flag.Int("mtu", 1400, "MTU интерфейса")
		route = flag.String("route", "10.99.98.0/24", "тестовая подсеть для проверки маршрутов")
		hold  = flag.Duration("hold", 0, "подержать интерфейс поднятым перед удалением")
	)
	flag.Parse()

	if !mode.IsAdmin() {
		fmt.Fprintln(os.Stderr, "FAIL: нужны права администратора (TUN-режим без них невозможен)")
		os.Exit(1)
	}
	fmt.Println("PASS: права администратора есть")

	dev, err := tun.Create(tun.Config{Name: *name, Address: *addr, MTU: *mtu})
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: создание TUN-адаптера: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("PASS: адаптер %q создан, адрес %s, MTU %d\n", dev.Name, *addr, *mtu)

	failures := 0
	if err := routing.AddRoute(*route, *addr, dev.Name); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: добавление маршрута %s: %v\n", *route, err)
		failures++
	} else {
		fmt.Printf("PASS: маршрут %s через %s добавлен\n", *route, dev.Name)
		if err := routing.DeleteRoute(*route, *addr, dev.Name); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: снятие маршрута %s: %v\n", *route, err)
			failures++
		} else {
			fmt.Printf("PASS: маршрут %s снят\n", *route)
		}
	}

	if *hold > 0 {
		fmt.Printf("держу адаптер поднятым %s...\n", *hold)
		time.Sleep(*hold)
	}

	if err := dev.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: закрытие адаптера: %v\n", err)
		failures++
	} else {
		fmt.Println("PASS: адаптер удалён")
	}

	if failures > 0 {
		fmt.Fprintf(os.Stderr, "ИТОГ: %d проверок провалено\n", failures)
		os.Exit(failures)
	}
	fmt.Println("ИТОГ: TUN-путь работает")
}
