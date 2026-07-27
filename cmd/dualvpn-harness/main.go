// Package main (dualvpn-harness) — headless-драйвер стенда: поднимает
// туннели через боевой vpn.Manager без Wails. Логика вынесена в
// dualvpn/test/e2e/harness — этот файл лишь разбирает флаги и печатает
// результат базовой проверки связности.
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
	"dualvpn/test/e2e/harness"
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

	m, ids, err := harness.Run(ctx, harness.Options{
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
		status, body, err := harness.GetWithRetry(client, t.ProbeURL, 15, time.Second)
		if err != nil || status != 200 {
			log.Printf("FAIL [%s] %s -> status=%d err=%v", t.Name, t.ProbeURL, status, err)
			failures++
			continue
		}
		log.Printf("PASS [%s] %s -> 200; тело: %.80s", t.Name, t.ProbeURL, body)
	}
	_ = ids

	// Кросс-проверка изоляции: пока мосты живы (процесс ещё не завершился),
	// SOCKS5-порт каждого туннеля не должен доставать до сети другого —
	// зеркалит проверку из test/e2e/mockasa_e2e_test.go. Постфактум-curl
	// в run.sh бил по уже закрытому порту после выхода процесса, поэтому
	// проверка перенесена сюда, где gVisor-мосты ещё подняты.
	if mode == sslcon.ModeSOCKS5 {
		failures += checkIsolation(cfg.Tunnels, log.Printf)
	}

	if failures > 0 {
		os.Exit(failures)
	}
}

// checkIsolation проверяет, что SOCKS5-порт каждого туннеля не достаёт до
// probe_url другого туннеля. Успешный (200, без ошибки) ответ через чужой
// туннель — нарушение изоляции и засчитывается как failure немедленно;
// пара коротких повторов нужна только чтобы не словить ложный proval из-за
// старта моста, а не для того, чтобы "дожидаться" пробоя.
func checkIsolation(tunnels []config.Tunnel, logf func(string, ...any)) int {
	failures := 0
	for i, ti := range tunnels {
		if ti.ProbeURL == "" || ti.SocksPort == 0 {
			continue
		}
		for j, tj := range tunnels {
			if i == j || tj.ProbeURL == "" {
				continue
			}
			client, err := checks.SocksClient(fmt.Sprintf("127.0.0.1:%d", ti.SocksPort), 3*time.Second)
			if err != nil {
				logf("[isolation] socks-клиент %s: %v", ti.Name, err)
				failures++
				continue
			}

			breach := false
			var lastErr error
			var lastStatus int
			for attempt := 0; attempt < 2; attempt++ {
				status, _, err := checks.GetBody(client, tj.ProbeURL)
				lastStatus, lastErr = status, err
				if err == nil && status == 200 {
					breach = true
					break
				}
				if attempt == 0 {
					time.Sleep(300 * time.Millisecond)
				}
			}

			if breach {
				logf("FAIL [isolation] туннель %s достиг сети %s", ti.Name, tj.Name)
				failures++
			} else {
				logf("PASS [isolation] сеть %s недоступна через туннель %s (status=%d err=%v)", tj.Name, ti.Name, lastStatus, lastErr)
			}
		}
	}
	return failures
}
