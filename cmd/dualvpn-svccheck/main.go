// Package main (dualvpn-svccheck) — проверка службы DualVPN из обычной
// пользовательской сессии: поднимает туннель через службу и печатает события.
//
// Смысл проверки — убедиться, что режим TUN работает БЕЗ прав администратора:
// адаптер, маршруты и DNS настраивает служба.
//
//	dualvpn-svccheck -config config.toml -tunnel MT -hold 30s
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"dualvpn/internal/config"
	"dualvpn/internal/ipc"
)

func main() {
	var (
		cfgPath = flag.String("config", "config.toml", "путь к config.toml")
		name    = flag.String("tunnel", "", "имя туннеля из конфигурации")
		hold    = flag.Duration("hold", 30*time.Second, "держать туннель поднятым")
		otp     = flag.String("otp", "", "код второго фактора, если сервер его запросит")
	)
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("конфиг: %v", err)
	}
	var tun *config.Tunnel
	for i := range cfg.Tunnels {
		if cfg.Tunnels[i].Name == *name {
			tun = &cfg.Tunnels[i]
		}
	}
	if tun == nil {
		log.Fatalf("туннель %q не найден в конфигурации", *name)
	}

	client, err := ipc.Dial(5 * time.Second)
	if err != nil {
		log.Fatalf("служба недоступна: %v", err)
	}
	defer client.Close() //nolint:errcheck

	ver, err := client.Version()
	if err != nil {
		log.Fatalf("версия службы: %v", err)
	}
	log.Printf("служба отвечает, версия %s", ver)

	go func() {
		for ev := range client.Events() {
			log.Printf("[%s] %s: %s", ev.TunnelID, ev.Type, ev.Message)
			if ev.Type == "2fa" && *otp != "" {
				if err := client.Submit2FA(ev.TunnelID, *otp); err != nil {
					log.Printf("отправка кода: %v", err)
				}
			}
		}
	}()

	if err := client.Connect(ipc.ConnectParams{
		ID:       tun.Name,
		Host:     tun.Endpoint,
		Group:    tun.Group,
		Username: tun.Username,
		Password: tun.Password,
		TunName:  tun.TunName,
		Routes:   tun.Routes,
	}); err != nil {
		log.Fatalf("подключение через службу: %v", err)
	}
	log.Printf("запрос на подключение принят службой, держу %s", *hold)

	time.Sleep(*hold)

	states, err := client.Status()
	if err != nil {
		log.Printf("состояние: %v", err)
	} else {
		for _, st := range states {
			log.Printf("состояние: %s подключён=%v", st.ID, st.Connected)
		}
	}

	if err := client.Disconnect(tun.Name); err != nil {
		log.Printf("отключение: %v (тип %T)", err, err)
		if _, verr := client.Version(); verr != nil {
			log.Printf("связь после ошибки: %v", verr)
		} else {
			log.Print("связь после ошибки жива")
		}
		os.Exit(1)
	}
	log.Print("отключено")
	time.Sleep(2 * time.Second) // дать событиям дойти
	fmt.Println("готово")
}
