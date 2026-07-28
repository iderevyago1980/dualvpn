// Package main (dualvpn-harness) — headless-драйвер стенда: поднимает
// туннели через боевой vpn.Manager без Wails. Логика вынесена в
// dualvpn/test/e2e/harness — этот файл лишь разбирает флаги и печатает
// результат базовой проверки связности.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"dualvpn/internal/config"
	"dualvpn/internal/vpn"
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
		otp      = flag.String("otp", "", "код второго фактора; пусто — запросить с stdin")
		hold     = flag.Duration("hold", 0, "держать туннели поднятыми указанное время после проверок")
		resolve  = flag.String("resolve", "", "имена через запятую: разрешить через DNS каждого туннеля")
		groups   = flag.Bool("groups", false, "только показать группы, предлагаемые серверами, и выйти")
		pacPort  = flag.Int("pac", 0, "поднять раздачу PAC на этом порту (-1 — свободный порт) и напечатать скрипт")
	)
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("конфиг: %v", err)
	}

	// Список групп приходит до логина — учётные данные и 2FA не нужны.
	if *groups {
		os.Exit(printGroups(cfg))
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
		OnTwoFA: twoFAReader(*otp),
	})
	if err != nil {
		log.Fatalf("подъём туннелей: %v", err)
	}
	defer m.StopAll()

	failures := 0

	// PAC: правила собираются из split-DNS и split-include поднятых туннелей.
	if *pacPort != 0 {
		port := *pacPort
		if port < 0 {
			port = 0 // свободный порт
		}
		url, err := m.EnablePAC(port)
		if err != nil {
			log.Printf("FAIL [pac] %v", err)
			failures++
		} else {
			log.Printf("PASS [pac] %s\n%s", url, m.PACScript())
		}
	}

	// Проверка DNS: разрешаем имена через DNS-серверы каждого туннеля.
	if *resolve != "" {
		failures += checkResolve(ctx, m, cfg, strings.Split(*resolve, ","))
	}

	// Базовая проверка связности: в socks5 — GET через каждый порт на его probe-url.
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

	// Туннели живут, пока жив процесс: -hold позволяет попользоваться
	// SOCKS5-портами (или TUN-маршрутами) вручную после автопроверок.
	if *hold > 0 {
		log.Printf("держу туннели поднятыми %s (Ctrl+C — выход)", *hold)
		time.Sleep(*hold)
	}

	if failures > 0 {
		os.Exit(failures)
	}
}

// printGroups печатает группы, предлагаемые каждым сервером из конфига, и
// помечает, совпадает ли группа из конфига с одной из них: несовпадение —
// частая причина отказа ещё до ввода пароля.
func printGroups(cfg *config.Config) int {
	failures := 0
	for _, t := range cfg.Tunnels {
		list, err := sslcon.FetchGroups(t.Endpoint, false)
		if err != nil {
			log.Printf("FAIL [%s] %s: %v", t.Name, t.Endpoint, err)
			failures++
			continue
		}
		if len(list) == 0 {
			log.Printf("[%s] %s: сервер не предлагает выбор группы", t.Name, t.Endpoint)
			continue
		}
		mark := "НЕ НАЙДЕНА в списке"
		for _, g := range list {
			if g == t.Group {
				mark = "ok"
				break
			}
		}
		if t.Group == "" {
			mark = "не задана — будет использована группа сервера по умолчанию"
		}
		log.Printf("[%s] %s: %s | группа из конфига %q — %s",
			t.Name, t.Endpoint, strings.Join(list, " | "), t.Group, mark)
		if mark == "НЕ НАЙДЕНА в списке" {
			failures++
		}
	}
	return failures
}

// checkResolve разрешает имена через DNS каждого туннеля и печатает результат.
// Показывает и сами адреса DNS-серверов, полученные от шлюза, — если их нет,
// причина не в резолвере, а в том, что сервер их не прислал.
func checkResolve(ctx context.Context, m *vpn.Manager, cfg *config.Config, names []string) int {
	failures := 0
	for _, t := range cfg.Tunnels {
		for _, raw := range names {
			name := strings.TrimSpace(raw)
			if name == "" {
				continue
			}
			res, err := m.LookupIP(ctx, t.Name, name)
			if err != nil {
				log.Printf("FAIL [dns %s] %s -> %v (источник: %s, DNS туннеля: %v)",
					t.Name, name, err, res.Source, res.Servers)
				failures++
				continue
			}
			log.Printf("PASS [dns %s] %s -> %s (источник: %s, DNS туннеля: %v)",
				t.Name, name, res.IP, res.Source, res.Servers)
		}
	}
	return failures
}

// twoFAReader возвращает обработчик запроса второго фактора: если код задан
// флагом -otp, отдаёт его (только для первого туннеля — TOTP одноразовый),
// иначе спрашивает с stdin. Пустой ответ считается отказом.
func twoFAReader(preset string) func(string, string) (string, error) {
	in := bufio.NewReader(os.Stdin)
	used := false
	return func(tunnelID, message string) (string, error) {
		if preset != "" && !used {
			used = true
			return preset, nil
		}
		fmt.Fprintf(os.Stderr, "[%s] %s\nвведите код: ", tunnelID, message)
		line, err := in.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("чтение кода со stdin: %w", err)
		}
		code := strings.TrimSpace(line)
		if code == "" {
			return "", errors.New("код не введён")
		}
		return code, nil
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
