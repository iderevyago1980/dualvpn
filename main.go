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
	"dualvpn/internal/socks5"
	"dualvpn/internal/vpn"
)

func main() {
	cfgPath := flag.String("config", "config.toml", "путь к TOML-конфигу")
	connect := flag.Bool("connect", false, "реально запускать openconnect (иначе dry-run)")
	flag.Parse()

	cfg, err := loadOrCreate(*cfgPath)
	if err != nil {
		log.Fatalf("конфигурация: %v", err)
	}

	fmt.Printf("DualVPN — режим: %s, туннелей: %d\n\n", cfg.Mode.Preferred, len(cfg.Tunnels))
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
			runTunnel(ctx, t)
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

// runTunnel поднимает SOCKS5-сервер и процесс openconnect для одного туннеля,
// транслируя события статуса в лог до завершения процесса или отмены контекста.
func runTunnel(ctx context.Context, t config.Tunnel) {
	logf := func(format string, a ...any) {
		log.Printf("[%s] %s", t.Name, fmt.Sprintf(format, a...))
	}

	// SOCKS5-прокси туннеля (пока с прямым dialer — интеграция netstack в этапе 3).
	srv, err := socks5.New(t.SocksPort, nil)
	if err != nil {
		logf("SOCKS5: %v", err)
		return
	}
	if err := srv.Start(); err != nil {
		logf("SOCKS5: %v", err)
		return
	}
	defer srv.Stop() //nolint:errcheck
	logf("SOCKS5-прокси слушает %s", srv.Addr())

	client := vpn.New(vpn.Options{
		Server:   t.Endpoint,
		Group:    t.Group,
		Username: t.Username,
		Password: t.Password,
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
