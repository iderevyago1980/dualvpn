package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"dualvpn/internal/config"
	"dualvpn/internal/vpn/sslcon"
	"dualvpn/test/e2e/checks"
)

func main() {
	var (
		cfgPath  = flag.String("config", "config.toml", "путь к config.toml")
		modeFlag = flag.String("mode", "socks5", "режим: socks5 | tun")
		insecure = flag.Bool("insecure", true, "не проверять TLS-сертификат сервера (стенд)")
		timeout  = flag.Duration("timeout", 30*time.Second, "таймаут готовности туннелей")
	)
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("конфиг: %v", err)
	}

	mode := sslcon.ModeSOCKS5
	if *modeFlag == "tun" {
		mode = sslcon.ModeTUN
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m, ids, err := run(ctx, Options{
		Cfg: cfg, Mode: mode, Insecure: *insecure,
		ReadyTimeout: *timeout, Logf: log.Printf,
	})
	if err != nil {
		log.Fatalf("подъём туннелей: %v", err)
	}
	defer m.StopAll()

	// Базовая проверка связности: в socks5 — GET через каждый порт на его probe-url.
	failures := 0
	for _, t := range cfg.Tunnels {
		if t.ProbeURL == "" {
			continue
		}
		var client *http.Client
		if mode == sslcon.ModeSOCKS5 {
			client, err = checks.SocksClient(fmt.Sprintf("127.0.0.1:%d", t.SocksPort), 10*time.Second)
			if err != nil {
				log.Printf("[%s] socks-клиент: %v", t.Name, err)
				failures++
				continue
			}
		} else {
			client = checks.DirectClient(10 * time.Second)
		}
		status, body, err := getWithRetry(client, t.ProbeURL, 15, time.Second)
		if err != nil || status != 200 {
			log.Printf("FAIL [%s] %s -> status=%d err=%v", t.Name, t.ProbeURL, status, err)
			failures++
			continue
		}
		log.Printf("PASS [%s] %s -> 200; тело: %.80s", t.Name, t.ProbeURL, body)
	}
	_ = ids
	if failures > 0 {
		os.Exit(failures)
	}
}

// getWithRetry повторяет GET, пока не получит 200 либо не исчерпает попытки:
// SOCKS5-мост становится готов чуть позже события Connected.
func getWithRetry(client *http.Client, url string, attempts int, pause time.Duration) (int, string, error) {
	var lastErr error
	var lastStatus int
	for i := 0; i < attempts; i++ {
		status, body, err := checks.GetBody(client, url)
		if err == nil && status == 200 {
			return status, body, nil
		}
		lastErr, lastStatus = err, status
		time.Sleep(pause)
	}
	return lastStatus, "", lastErr
}
