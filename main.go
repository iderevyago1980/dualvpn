// DualVPN — одновременное подключение к двум Cisco AnyConnect VPN.
// Точка входа CLI-каркаса: загружает конфиг, создаёт туннели, печатает статус.
//
// Реальное подключение запускается только с флагом -connect;
// по умолчанию выполняется «сухой» прогон (показ конфигурации).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"dualvpn/internal/config"
	"dualvpn/internal/mode"
	"dualvpn/internal/vpn"
)

func main() {
	cfgPath := flag.String("config", "config.toml", "путь к TOML-конфигу")
	connect := flag.Bool("connect", false, "реально запускать openconnect (иначе dry-run)")
	modeFlag := flag.String("mode", "auto", "режим работы: auto|tun|socks5")
	flag.Parse()

	cfg, err := loadOrCreate(*cfgPath)
	if err != nil {
		log.Fatalf("конфигурация: %v", err)
	}

	runMode := resolveMode(*modeFlag, cfg.Mode.Preferred)
	log.Printf("режим работы: %s (флаг=%s, конфиг=%s, admin=%v)",
		runMode, *modeFlag, cfg.Mode.Preferred, mode.IsAdmin())

	fmt.Printf("DualVPN — режим: %s, туннелей: %d\n\n", runMode, len(cfg.Tunnels))
	for _, t := range cfg.Tunnels {
		fmt.Printf("  [%s] %s (группа %q) → SOCKS5 127.0.0.1:%d, TUN %s, маршруты %v\n",
			t.Name, t.Endpoint, t.Group, t.SocksPort, t.TunName, t.Routes)
	}

	if !*connect {
		fmt.Println("\nDry-run: запустите с флагом -connect для реального подключения.")
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var wg sync.WaitGroup
	for _, t := range cfg.Tunnels {
		wg.Add(1)
		go func(t config.Tunnel) {
			defer wg.Done()
			runTunnel(ctx, t, runMode)
		}(t)
	}
	wg.Wait()
	fmt.Println("Все туннели завершены.")
}

// loadOrCreate загружает конфиг; если файла нет — создаёт из значений по умолчанию.
func loadOrCreate(path string) (*config.Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		cfg := config.Default()
		if err := cfg.Save(path); err != nil {
			return nil, err
		}
		log.Printf("создан конфиг по умолчанию: %s", path)
		return cfg, nil
	}
	return config.Load(path)
}

// resolveMode выбирает итоговый режим работы: флаг CLI имеет приоритет
// над конфигом, значение "auto" разрешается автодетекцией админ-прав.
func resolveMode(flagMode, cfgMode string) string {
	m := flagMode
	if m == "auto" {
		m = cfgMode
	}
	if m == "auto" || m == "" {
		m = mode.Detect()
	}
	return m
}

// runTunnel запускает процесс openconnect для одного туннеля и транслирует
// события статуса в лог до завершения процесса или отмены контекста.
//
// В режиме socks5 отдельный SOCKS5-сервер не нужен: openconnect запускает
// ocproxy (--script-tun), который сам поднимает SOCKS5 на порту туннеля.
// В режиме tun openconnect создаёт TUN-интерфейс через vpnc-script.
func runTunnel(ctx context.Context, t config.Tunnel, runMode string) {
	logf := func(format string, a ...any) {
		log.Printf("[%s] %s", t.Name, fmt.Sprintf(format, a...))
	}

	if runMode == "socks5" {
		logf("SOCKS5 (ocproxy) будет слушать 127.0.0.1:%d", t.SocksPort)
	} else {
		logf("TUN-интерфейс: %s", t.TunName)
	}

	client := vpn.New(vpn.Options{
		Server:    t.Endpoint,
		Group:     t.Group,
		Username:  t.Username,
		Password:  t.Password,
		Mode:      runMode,
		SocksPort: t.SocksPort,
	})
	if err := client.Start(ctx); err != nil {
		logf("openconnect: %v", err)
		return
	}
	defer client.Stop() //nolint:errcheck

	for {
		select {
		case <-ctx.Done():
			logf("остановка по сигналу")
			return
		case ev, ok := <-client.Events():
			if !ok {
				return
			}
			logf("%s: %s", ev.Type, ev.Message)
			if ev.Type == vpn.Event2FARequired {
				// Каркас: код читается из stdin; в UI здесь будет модальный диалог.
				fmt.Printf("[%s] Введите 2FA-код: ", t.Name)
				var code string
				fmt.Scanln(&code) //nolint:errcheck
				client.Submit2FA(code)
			}
		}
	}
}
