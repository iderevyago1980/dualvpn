# DualVPN E2E Linux-ядро (host-прогон) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Собрать переиспользуемый headless-стенд, который поднимает два одновременных туннеля DualVPN против AnyConnect-совместимых серверов и проверяет связность + изоляцию в режимах SOCKS5 и TUN — сначала на `mockasa` (чистый `go test`), затем на `ocserv` (docker), всё запускается на хосте через `make e2e`.

**Architecture:** Новый бинарь `cmd/dualvpn-harness` переиспользует боевой `vpn.Manager`/`sslcon` без Wails: читает `config.toml`, поднимает оба туннеля, ждёт готовности, гоняет проверки из пакета `test/e2e/checks`. Серверная сторона подключаемая: `mockasa` (в процессе теста) и `ocserv` (docker-compose, публикуется на `127.0.0.1:4443/4444`). Клиент ходит на серверы по `host:port` с `InsecureSkipVerify` — без bridge/tap и без sudo для сети (кроме TUN-режима, которому нужен root на самом хосте).

**Tech Stack:** Go 1.26, `dualvpn/internal/vpn` (Manager, sslcon), `dualvpn/internal/mockasa`, `armon/go-socks5` (уже в зависимостях), `golang.org/x/net/proxy` (SOCKS5-клиент), Docker + `ocserv`, `traefik/whoami`, штатный `openconnect` как эталон.

## Global Constraints

- Go **не в PATH** — каждый прогон: `export PATH="/usr/local/go/bin:$PATH"`.
- Тег `webkit2_41` нужен только коду с Wails (`internal/ui`). Харнесс и `test/e2e` его **не** импортируют, поэтому `go test ./test/e2e/... ./cmd/...` собирается **без** тега. `go build ./...` (весь модуль) по-прежнему требует `GOFLAGS="-tags=webkit2_41"`.
- Гонки в многотуннельном коде: тесты E2E запускать `-race`, milestone-тест — `-count=3`.
- Всегда ставить `-timeout` на E2E-тесты (при зависании иначе висят): `-timeout 60s`.
- Комментарии и сообщения — на русском (конвенция репозитория).
- Модульный путь: `dualvpn`. Существующие `vpn.Manager`, `sslcon`, `mockasa`, `config` — переиспользуются **без изменений**.
- `config.Tunnel.Endpoint` кладётся в `sslcon.ClientConfig.Host`, который поддерживает `host:port`.

---

## Структура файлов

- Create: `test/e2e/checks/checks.go` — HTTP-GET напрямую и через SOCKS5, построение SOCKS5-клиента.
- Create: `test/e2e/checks/checks_test.go` — юнит-тесты пакета.
- Create: `cmd/dualvpn-harness/config.go` — маппинг `config.Config` → `[]vpn.TunnelConfig`.
- Create: `cmd/dualvpn-harness/config_test.go` — юнит-тест маппинга.
- Create: `cmd/dualvpn-harness/harness.go` — `run()`: подъём туннелей, ожидание готовности, exit-код.
- Create: `cmd/dualvpn-harness/main.go` — разбор флагов, вызов `run()`.
- Create: `test/e2e/mockasa_e2e_test.go` — milestone: два mockasa + харнесс + изоляция.
- Create: `test/e2e/backends/ocserv/docker-compose.yml`
- Create: `test/e2e/backends/ocserv/ocserv-a.conf`, `ocserv-b.conf`
- Create: `test/e2e/backends/ocserv/gen-certs.sh` — локальный CA + серверные серты.
- Create: `test/e2e/backends/ocserv/up.sh`, `test/e2e/backends/ocserv/down.sh`
- Create: `test/e2e/run.sh` — оркестрация host-прогона + teardown.
- Modify: `Makefile` — цель `e2e`.
- Modify: `.gitignore` — серты/артефакты стенда.
- Modify: `go.mod`/`go.sum` — добавить `golang.org/x/net/proxy` (через `go get`).

---

### Task 1: Пакет проверок `test/e2e/checks`

**Files:**
- Create: `test/e2e/checks/checks.go`
- Test: `test/e2e/checks/checks_test.go`

**Interfaces:**
- Consumes: `golang.org/x/net/proxy`, `net/http`.
- Produces:
  - `func SocksClient(proxyAddr string, timeout time.Duration) (*http.Client, error)` — `*http.Client`, чей транспорт диалит через SOCKS5 `proxyAddr` (формат `host:port`).
  - `func GetBody(client *http.Client, url string) (status int, body string, err error)` — GET, читает тело целиком.
  - `func DirectClient(timeout time.Duration) *http.Client` — обычный клиент (для TUN-режима).

- [ ] **Step 1: Добавить зависимость x/net/proxy**

Run:
```bash
export PATH="/usr/local/go/bin:$PATH"
cd /home/ub/dualvpn && go get golang.org/x/net/proxy@latest
```
Expected: `go.mod` получает `golang.org/x/net`; `go.sum` обновлён.

- [ ] **Step 2: Написать падающий тест**

Создать `test/e2e/checks/checks_test.go`:
```go
package checks

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	socks5 "github.com/armon/go-socks5"
)

// TestGetBodyDirect — прямой GET возвращает статус и тело.
func TestGetBodyDirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("hello-" + r.RemoteAddr))
	}))
	defer srv.Close()

	status, body, err := GetBody(DirectClient(5*time.Second), srv.URL)
	if err != nil {
		t.Fatalf("GetBody: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, ожидалось 200", status)
	}
	if len(body) == 0 || body[:6] != "hello-" {
		t.Fatalf("body = %q, ожидался префикс hello-", body)
	}
}

// TestGetBodyViaSocks — GET через реальный SOCKS5-прокси доходит до цели.
func TestGetBodyViaSocks(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("via-socks"))
	}))
	defer target.Close()

	// Поднимаем настоящий SOCKS5-сервер на свободном порту.
	sconf := &socks5.Config{}
	sserv, err := socks5.New(sconf)
	if err != nil {
		t.Fatalf("socks5.New: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() { _ = sserv.Serve(ln) }()

	client, err := SocksClient(ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("SocksClient: %v", err)
	}
	status, body, err := GetBody(client, target.URL)
	if err != nil {
		t.Fatalf("GetBody via socks: %v", err)
	}
	if status != 200 || body != "via-socks" {
		t.Fatalf("status=%d body=%q", status, body)
	}
}
```

- [ ] **Step 3: Прогнать — убедиться, что не компилируется/падает**

Run:
```bash
export PATH="/usr/local/go/bin:$PATH"
cd /home/ub/dualvpn && go test ./test/e2e/checks/ -run TestGetBody -v -timeout 30s
```
Expected: FAIL — `undefined: GetBody / DirectClient / SocksClient`.

- [ ] **Step 4: Реализовать пакет**

Создать `test/e2e/checks/checks.go`:
```go
// Package checks — переиспользуемые сетевые проверки E2E-стенда:
// HTTP-GET напрямую (TUN) и через SOCKS5-прокси (режим socks5).
package checks

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/proxy"
)

// DirectClient — обычный HTTP-клиент с заданным таймаутом (для TUN-режима,
// где маршрут до внутренней сети уже в таблице маршрутизации ОС).
func DirectClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// SocksClient строит HTTP-клиент, весь трафик которого идёт через SOCKS5-прокси
// proxyAddr (формат "host:port") — точку, поднятую туннелем в режиме socks5.
func SocksClient(proxyAddr string, timeout time.Duration) (*http.Client, error) {
	dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("socks5-диалер %s: %w", proxyAddr, err)
	}
	ctxDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("socks5-диалер не поддерживает контекст")
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return ctxDialer.DialContext(ctx, network, addr)
		},
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

// GetBody выполняет GET и возвращает статус-код и тело ответа целиком.
func GetBody(client *http.Client, url string) (int, string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(body), nil
}
```

- [ ] **Step 5: Прогнать — зелёный**

Run:
```bash
export PATH="/usr/local/go/bin:$PATH"
cd /home/ub/dualvpn && go test ./test/e2e/checks/ -v -race -timeout 30s
```
Expected: PASS (оба теста).

- [ ] **Step 6: Commit**

```bash
cd /home/ub/dualvpn
git add test/e2e/checks/ go.mod go.sum
git commit -m "test(e2e): пакет checks — HTTP-GET напрямую и через SOCKS5"
```

---

### Task 2: Маппинг конфига в харнессе

**Files:**
- Create: `cmd/dualvpn-harness/config.go`
- Test: `cmd/dualvpn-harness/config_test.go`

**Interfaces:**
- Consumes: `dualvpn/internal/config` (`config.Config`, `config.Tunnel`), `dualvpn/internal/vpn` (`vpn.TunnelConfig`), `dualvpn/internal/vpn/sslcon` (`sslcon.ClientConfig`).
- Produces:
  - `func buildConfigs(cfg *config.Config, mode string, insecure bool) []vpn.TunnelConfig` — зеркалит `ui.App.registerTunnels`, но режим и insecure задаются извне (стенд), ID туннеля = имя.

- [ ] **Step 1: Написать падающий тест**

Создать `cmd/dualvpn-harness/config_test.go`:
```go
package main

import (
	"testing"

	"dualvpn/internal/config"
)

func TestBuildConfigs(t *testing.T) {
	cfg := &config.Config{
		Tunnels: []config.Tunnel{
			{Name: "a", Endpoint: "127.0.0.1:4443", Group: "GA", Username: "u1", Password: "p1", SocksPort: 1080, TunName: "ta", Routes: []string{"192.168.90.0/24"}},
			{Name: "b", Endpoint: "127.0.0.1:4444", Group: "GB", Username: "u2", Password: "p2", SocksPort: 1081, TunName: "tb", Routes: []string{"192.168.91.0/24"}},
		},
	}
	got := buildConfigs(cfg, "socks5", true)
	if len(got) != 2 {
		t.Fatalf("len = %d, ожидалось 2", len(got))
	}
	if got[0].ID != "a" || got[0].Opts.Host != "127.0.0.1:4443" {
		t.Fatalf("t0 = %+v", got[0])
	}
	if got[0].Opts.Group != "GA" || got[0].Opts.Username != "u1" || got[0].Opts.Password != "p1" {
		t.Fatalf("t0 opts = %+v", got[0].Opts)
	}
	if !got[0].Opts.InsecureSkipVerify {
		t.Fatalf("insecure не проброшен")
	}
	if got[0].Mode != "socks5" || got[0].SocksPort != 1080 {
		t.Fatalf("t0 mode/port = %s/%d", got[0].Mode, got[0].SocksPort)
	}
	if got[1].Opts.TunName != "tb" || len(got[1].Routes) != 1 {
		t.Fatalf("t1 = %+v", got[1])
	}
}
```

- [ ] **Step 2: Прогнать — падает**

Run:
```bash
export PATH="/usr/local/go/bin:$PATH"
cd /home/ub/dualvpn && go test ./cmd/dualvpn-harness/ -run TestBuildConfigs -v -timeout 30s
```
Expected: FAIL — `undefined: buildConfigs`.

- [ ] **Step 3: Реализовать маппинг**

Создать `cmd/dualvpn-harness/config.go`:
```go
// Package main (dualvpn-harness) — headless-драйвер стенда: поднимает
// туннели через боевой vpn.Manager без Wails.
package main

import (
	"dualvpn/internal/config"
	"dualvpn/internal/vpn"
	"dualvpn/internal/vpn/sslcon"
)

// buildConfigs зеркалит ui.App.registerTunnels, но режим и insecure задаются
// стендом (не автодетекцией). ID туннеля = имя из конфига.
func buildConfigs(cfg *config.Config, mode string, insecure bool) []vpn.TunnelConfig {
	cfgs := make([]vpn.TunnelConfig, 0, len(cfg.Tunnels))
	for _, t := range cfg.Tunnels {
		cfgs = append(cfgs, vpn.TunnelConfig{
			ID: t.Name,
			Opts: sslcon.ClientConfig{
				Host:               t.Endpoint,
				Group:              t.Group,
				Username:           t.Username,
				Password:           t.Password,
				TunName:            t.TunName,
				InsecureSkipVerify: insecure,
			},
			Routes:    t.Routes,
			Mode:      mode,
			SocksPort: t.SocksPort,
		})
	}
	return cfgs
}
```

- [ ] **Step 4: Прогнать — зелёный**

Run:
```bash
export PATH="/usr/local/go/bin:$PATH"
cd /home/ub/dualvpn && go test ./cmd/dualvpn-harness/ -run TestBuildConfigs -v -timeout 30s
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/ub/dualvpn
git add cmd/dualvpn-harness/config.go cmd/dualvpn-harness/config_test.go
git commit -m "feat(harness): маппинг config.Config -> vpn.TunnelConfig"
```

---

### Task 3: Ядро харнесса `run()` + `main`

**Files:**
- Create: `cmd/dualvpn-harness/harness.go`
- Create: `cmd/dualvpn-harness/main.go`

**Interfaces:**
- Consumes: `buildConfigs` (Task 2), `vpn.NewManager/ReplaceTunnels/StartAll/StopAll/Status/Events`.
- Produces:
  - `type Options struct { Cfg *config.Config; Mode string; Insecure bool; ReadyTimeout time.Duration; Logf func(string, ...any) }`
  - `func waitReady(ctx context.Context, m *vpn.Manager, ids []string, timeout time.Duration, logf func(string, ...any)) error` — ждёт, пока `Status(id)` вернёт `connected==true` по всем `ids`, иначе ошибка по таймауту. Потребляет `m.Events()` и логирует их.
  - `func run(ctx context.Context, opts Options) (*vpn.Manager, []string, error)` — регистрирует туннели, `StartAll`, `waitReady`; возвращает менеджер и id для последующих проверок (проверки связности делает вызывающий, т.к. они зависят от бэкенда). Останов — на вызывающем (`defer m.StopAll()`).

Замечание по семантике: `sslcon` эмитит `EventConnected` несколько раз (TLS → auth → socks-listen), поэтому единственный надёжный признак готовности — опрос `Status(id)`; события только логируем.

- [ ] **Step 1: Реализовать `harness.go`**

Создать `cmd/dualvpn-harness/harness.go`:
```go
package main

import (
	"context"
	"fmt"
	"time"

	"dualvpn/internal/config"
	"dualvpn/internal/vpn"
)

// Options — параметры запуска харнесса.
type Options struct {
	Cfg          *config.Config
	Mode         string
	Insecure     bool
	ReadyTimeout time.Duration
	Logf         func(string, ...any)
}

// run регистрирует туннели, запускает их и ждёт готовности всех.
// Проверки связности выполняет вызывающий (они зависят от бэкенда).
// Останавливать менеджер — тоже вызывающему: defer m.StopAll().
func run(ctx context.Context, opts Options) (*vpn.Manager, []string, error) {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	cfgs := buildConfigs(opts.Cfg, opts.Mode, opts.Insecure)
	ids := make([]string, 0, len(cfgs))
	for _, c := range cfgs {
		ids = append(ids, c.ID)
	}

	m := vpn.NewManager()
	m.ReplaceTunnels(cfgs)

	// Стартуем в отдельной горутине: StartAll блокируется до Connect каждого.
	go m.StartAll(ctx)

	if err := waitReady(ctx, m, ids, opts.ReadyTimeout, opts.Logf); err != nil {
		m.StopAll()
		return nil, nil, err
	}
	return m, ids, nil
}

// waitReady потребляет события менеджера (для лога) и опрашивает Status,
// пока все туннели не станут connected либо не выйдет таймаут.
func waitReady(ctx context.Context, m *vpn.Manager, ids []string, timeout time.Duration, logf func(string, ...any)) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case ev := <-m.Events():
			logf("[%s] %s: %s", ev.TunnelID, ev.Event.Type, ev.Event.Message)
		case <-tick.C:
			allUp := true
			for _, id := range ids {
				up, _ := m.Status(id)
				if !up {
					allUp = false
					break
				}
			}
			if allUp {
				logf("все туннели готовы: %v", ids)
				return nil
			}
		case <-deadline.C:
			var down []string
			for _, id := range ids {
				if up, _ := m.Status(id); !up {
					down = append(down, id)
				}
			}
			return fmt.Errorf("таймаут готовности (%s), не поднялись: %v", timeout, down)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
```

- [ ] **Step 2: Реализовать `main.go`**

Создать `cmd/dualvpn-harness/main.go`:
```go
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
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
		var client = checks.DirectClient(10 * time.Second)
		if mode == sslcon.ModeSOCKS5 {
			client, err = checks.SocksClient(fmt.Sprintf("127.0.0.1:%d", t.SocksPort), 10*time.Second)
			if err != nil {
				log.Printf("[%s] socks-клиент: %v", t.Name, err)
				failures++
				continue
			}
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

// getWithRetry повторяет GET, пока не 200 или не исчерпаны попытки (готовность
// socks-моста наступает чуть позже Connected).
func getWithRetry(client interface {
	Get(string) (*httpResponse, error)
}, url string, attempts int, pause time.Duration) (int, string, error) {
	panic("заменяется в шаге 3")
}
```

Примечание: сигнатура `getWithRetry` в шаге 2 — заглушка (в `main` есть ссылка), в шаге 3 заменяем на корректную реализацию через `checks.GetBody`. Также нужен флаг конфига `ProbeURL` — добавляется в шаге 3.

- [ ] **Step 3: Добавить `ProbeURL` в конфиг и корректный `getWithRetry`**

В `internal/config/config.go` в структуру `Tunnel` добавить поле (после `Password`):
```go
	ProbeURL  string   `toml:"probe_url"` // URL внутри VPN для проверки связности на стенде (E2E)
```

Заменить в `cmd/dualvpn-harness/main.go` заглушку `getWithRetry` на:
```go
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
```
И заменить объявление `var client = ...` / `client, err = ...` так, чтобы `client` был `*http.Client`; добавить импорт `net/http`. Итоговый блок проверки в `main`:
```go
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
```

- [ ] **Step 4: Собрать харнесс**

Run:
```bash
export PATH="/usr/local/go/bin:$PATH"
cd /home/ub/dualvpn && go build -o bin/dualvpn-harness ./cmd/dualvpn-harness/ && echo OK
```
Expected: `OK`, бинарь `bin/dualvpn-harness` создан. (Тег webkit не нужен — харнесс не тянет `internal/ui`.)

- [ ] **Step 5: Проверить существующие тесты конфига не сломались**

Run:
```bash
export PATH="/usr/local/go/bin:$PATH"
cd /home/ub/dualvpn && go test ./internal/config/ ./cmd/dualvpn-harness/ -v -timeout 30s
```
Expected: PASS (новое поле `ProbeURL` не ломает загрузку).

- [ ] **Step 6: Commit**

```bash
cd /home/ub/dualvpn
git add cmd/dualvpn-harness/harness.go cmd/dualvpn-harness/main.go internal/config/config.go
git commit -m "feat(harness): run() + main, флаги mode/insecure/timeout, probe_url в конфиге"
```

---

### Task 4: Milestone — E2E-тест на двух `mockasa` (изоляция)

Полный путь `config → Manager(socks5) → два туннеля → внутренние HTTP-хосты → изоляция`, чистым `go test`, без docker/root.

**Files:**
- Create: `test/e2e/mockasa_e2e_test.go`

**Interfaces:**
- Consumes: `dualvpn/internal/mockasa` (`mockasa.New`, `Server.Addr`, `Server.HostIP`, `Server.StartHTTP`), `dualvpn/internal/config`, харнесс `run`/`Options`, `test/e2e/checks`.
- Produces: тест — конечный потребитель.

Как строится сценарий: два `mockasa.Server` с разными `HostIP`/`VPNAddress`/`SplitInclude`; на `HostIP:80` вешаем HTTP-обработчик, отдающий `r.RemoteAddr` (эмуляция whoami). Конфиг харнесса указывает `Endpoint = srv.Addr()` (mockasa слушает `127.0.0.1:port`), `ProbeURL = http://<HostIP>/`. Проверяем: оба connected, оба ProbeURL отвечают 200 через свой socks-порт, `RemoteAddr` на A принадлежит пулу A (`VPNAddress` A), и кросс-запрос (socks-порт A → HostIP B) **падает**.

- [ ] **Step 1: Написать тест**

Создать `test/e2e/mockasa_e2e_test.go`:
```go
package e2e

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"dualvpn/internal/config"
	"dualvpn/internal/mockasa"
	"dualvpn/internal/vpn/sslcon"
	"dualvpn/test/e2e/checks"
)

// startMock поднимает mockasa с внутренним HTTP-хостом, отдающим RemoteAddr.
func startMock(t *testing.T, hostIP, vpnAddr, split string) *mockasa.Server {
	t.Helper()
	srv, err := mockasa.New(mockasa.Config{
		Groups:       []string{"LAB"},
		Users:        map[string]string{"user": "pass"},
		VPNAddress:   vpnAddr,
		HostIP:       hostIP,
		SplitInclude: []string{split},
	})
	if err != nil {
		t.Fatalf("mockasa.New(%s): %v", hostIP, err)
	}
	h := http.NewServeMux()
	h.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("client=" + r.RemoteAddr))
	})
	if err := srv.StartHTTP(80, h); err != nil {
		srv.Close()
		t.Fatalf("StartHTTP(%s): %v", hostIP, err)
	}
	return srv
}

func TestDualTunnelSocksIsolation(t *testing.T) {
	srvA := startMock(t, "192.168.90.10", "10.90.0.2", "192.168.90.0/255.255.255.0")
	defer srvA.Close()
	srvB := startMock(t, "192.168.91.10", "10.91.0.2", "192.168.91.0/255.255.255.0")
	defer srvB.Close()

	cfg := &config.Config{
		Tunnels: []config.Tunnel{
			{Name: "a", Endpoint: srvA.Addr(), Group: "LAB", Username: "user", Password: "pass",
				SocksPort: 21080, ProbeURL: "http://192.168.90.10/"},
			{Name: "b", Endpoint: srvB.Addr(), Group: "LAB", Username: "user", Password: "pass",
				SocksPort: 21081, ProbeURL: "http://192.168.91.10/"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m, ids, err := run(ctx, Options{
		Cfg: cfg, Mode: sslcon.ModeSOCKS5, Insecure: true,
		ReadyTimeout: 20 * time.Second, Logf: t.Logf,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	defer m.StopAll()
	if len(ids) != 2 {
		t.Fatalf("ids = %v", ids)
	}

	// Связность + принадлежность пулу: A виден как 10.90.0.x, B как 10.91.0.x.
	cases := []struct {
		port       int
		url        string
		wantPrefix string
	}{
		{21080, "http://192.168.90.10/", "client=10.90.0."},
		{21081, "http://192.168.91.10/", "client=10.91.0."},
	}
	for _, c := range cases {
		cl, err := checks.SocksClient(fmt.Sprintf("127.0.0.1:%d", c.port), 10*time.Second)
		if err != nil {
			t.Fatalf("socks %d: %v", c.port, err)
		}
		status, body, err := getWithRetry(cl, c.url, 15, time.Second)
		if err != nil || status != 200 {
			t.Fatalf("порт %d -> %s: status=%d err=%v", c.port, c.url, status, err)
		}
		if !strings.HasPrefix(body, c.wantPrefix) {
			t.Fatalf("порт %d: тело %q, ожидался префикс %q", c.port, body, c.wantPrefix)
		}
	}

	// Изоляция: socks-порт A не должен доставать до сети B.
	clA, err := checks.SocksClient("127.0.0.1:21080", 5*time.Second)
	if err != nil {
		t.Fatalf("socks A: %v", err)
	}
	if status, _, err := checks.GetBody(clA, "http://192.168.91.10/"); err == nil && status == 200 {
		t.Fatalf("изоляция нарушена: через туннель A достигнута сеть B")
	}
}
```

Примечание: тест лежит в пакете `e2e`, а `run`/`Options`/`getWithRetry` объявлены в пакете `main` (харнесс). Чтобы тест их видел, в шаге 2 выносим общую логику в переиспользуемый пакет `test/e2e/harness` и заменяем импорт.

- [ ] **Step 2: Вынести `run`/`Options`/`getWithRetry` в пакет `test/e2e/harness`**

Создать пакет `test/e2e/harness/harness.go`, переместив туда из `cmd/dualvpn-harness/harness.go` и `main.go`: типы `Options`, функции `run`→`Run` (экспортировать), `waitReady`, `getWithRetry`→`GetWithRetry`, и `buildConfigs`→`BuildConfigs` (из `config.go`). Обновить:
- `cmd/dualvpn-harness/main.go` — импортировать `dualvpn/test/e2e/harness`, звать `harness.Run`, `harness.GetWithRetry`.
- `cmd/dualvpn-harness/config.go` и `config_test.go` — переместить в `test/e2e/harness/` (тест поправить на пакет `harness` и `BuildConfigs`).
- `test/e2e/mockasa_e2e_test.go` — импортировать `dualvpn/test/e2e/harness`; звать `harness.Run`, `harness.Options`, `harness.GetWithRetry`.

Экспортируемые сигнатуры в `test/e2e/harness`:
```go
func BuildConfigs(cfg *config.Config, mode string, insecure bool) []vpn.TunnelConfig
type Options struct { Cfg *config.Config; Mode string; Insecure bool; ReadyTimeout time.Duration; Logf func(string, ...any) }
func Run(ctx context.Context, opts Options) (*vpn.Manager, []string, error)
func GetWithRetry(client *http.Client, url string, attempts int, pause time.Duration) (int, string, error)
```
В тесте заменить `run(` → `harness.Run(`, `Options{` → `harness.Options{`, `getWithRetry(` → `harness.GetWithRetry(`.

- [ ] **Step 3: Прогнать milestone-тест**

Run:
```bash
export PATH="/usr/local/go/bin:$PATH"
cd /home/ub/dualvpn && go test ./test/e2e/ -run TestDualTunnelSocksIsolation -v -race -count=3 -timeout 90s 2>&1 | grep -vE '^\s*<' | tail -40
```
Expected: PASS во всех 3 прогонах (фильтр `grep -vE '^\s*<'` убирает XML-дебаг форкнутого sslcon).

- [ ] **Step 4: Прогнать весь харнесс+checks на сборку**

Run:
```bash
export PATH="/usr/local/go/bin:$PATH"
cd /home/ub/dualvpn && go build -o bin/dualvpn-harness ./cmd/dualvpn-harness/ && go vet ./test/e2e/... ./cmd/dualvpn-harness/ && echo OK
```
Expected: `OK`.

- [ ] **Step 5: Commit**

```bash
cd /home/ub/dualvpn
git add cmd/dualvpn-harness/ test/e2e/harness/ test/e2e/mockasa_e2e_test.go
git commit -m "test(e2e): milestone — два туннеля на mockasa + проверка изоляции (SOCKS5)"
```

---

### Task 5: Бэкенд `ocserv` (docker) + эталон `openconnect`

**Files:**
- Create: `test/e2e/backends/ocserv/gen-certs.sh`
- Create: `test/e2e/backends/ocserv/ocserv-a.conf`
- Create: `test/e2e/backends/ocserv/ocserv-b.conf`
- Create: `test/e2e/backends/ocserv/docker-compose.yml`
- Create: `test/e2e/backends/ocserv/up.sh`
- Create: `test/e2e/backends/ocserv/down.sh`
- Modify: `.gitignore`

**Interfaces:**
- Produces: два ocserv на `127.0.0.1:4443` и `127.0.0.1:4444`; за каждым — `traefik/whoami` на `192.168.90.10:80` / `192.168.91.10:80`; логин `user`/`pass`; split-include на inner-подсеть.

- [ ] **Step 1: Генератор сертификатов**

Создать `test/e2e/backends/ocserv/gen-certs.sh`:
```bash
#!/usr/bin/env bash
# Генерирует локальный CA и серверные сертификаты для ocserv-A/B (SAN=127.0.0.1).
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p certs && cd certs

if [[ -f ca.pem ]]; then echo "certs уже есть, пропускаю"; exit 0; fi

# CA
openssl req -x509 -newkey rsa:2048 -nodes -keyout ca-key.pem -out ca.pem \
  -days 3650 -subj "/CN=DualVPN-LAB-CA"

for n in a b; do
  openssl req -newkey rsa:2048 -nodes -keyout "server-$n-key.pem" \
    -out "server-$n.csr" -subj "/CN=ocserv-$n"
  openssl x509 -req -in "server-$n.csr" -CA ca.pem -CAkey ca-key.pem \
    -CAcreateserial -out "server-$n.pem" -days 3650 \
    -extfile <(printf "subjectAltName=IP:127.0.0.1,DNS:localhost")
  rm -f "server-$n.csr"
done
echo "сертификаты готовы в $(pwd)"
```
Сделать исполняемым: `chmod +x test/e2e/backends/ocserv/gen-certs.sh`.

- [ ] **Step 2: Конфиги ocserv**

Создать `test/e2e/backends/ocserv/ocserv-a.conf`:
```
auth = "plain[passwd=/etc/ocserv/passwd]"
tcp-port = 443
udp-port = 443
server-cert = /etc/ocserv/certs/server-a.pem
server-key = /etc/ocserv/certs/server-a-key.pem
socket-file = /run/ocserv-socket
max-clients = 8
max-same-clients = 4
try-mtu-discovery = false
default-domain = lab.local
ipv4-network = 10.90.0.0
ipv4-netmask = 255.255.255.0
route = 192.168.90.0/255.255.255.0
no-route = 0.0.0.0/0.0.0.0
cisco-client-compat = true
dtls-legacy = true
```
Создать `test/e2e/backends/ocserv/ocserv-b.conf` — идентично, но:
```
server-cert = /etc/ocserv/certs/server-b.pem
server-key = /etc/ocserv/certs/server-b-key.pem
ipv4-network = 10.91.0.0
route = 192.168.91.0/255.255.255.0
```
(остальные строки те же, что в `ocserv-a.conf`).

- [ ] **Step 3: docker-compose**

Создать `test/e2e/backends/ocserv/docker-compose.yml`:
```yaml
# Стенд ocserv: два сервера, каждый со своей изолированной inner-сетью и whoami.
services:
  ocserv-a:
    image: tiredofit/ocserv:latest
    cap_add: [NET_ADMIN]
    devices: ["/dev/net/tun:/dev/net/tun"]
    ports: ["127.0.0.1:4443:443/tcp"]
    volumes:
      - ./ocserv-a.conf:/etc/ocserv/ocserv.conf:ro
      - ./certs:/etc/ocserv/certs:ro
      - ./passwd:/etc/ocserv/passwd:ro
    command: ["ocserv", "-c", "/etc/ocserv/ocserv.conf", "-f"]
    networks: [wan, inner_a]
  whoami-a:
    image: traefik/whoami:latest
    command: ["--port=80"]
    networks:
      inner_a:
        ipv4_address: 192.168.90.10

  ocserv-b:
    image: tiredofit/ocserv:latest
    cap_add: [NET_ADMIN]
    devices: ["/dev/net/tun:/dev/net/tun"]
    ports: ["127.0.0.1:4444:443/tcp"]
    volumes:
      - ./ocserv-b.conf:/etc/ocserv/ocserv.conf:ro
      - ./certs:/etc/ocserv/certs:ro
      - ./passwd:/etc/ocserv/passwd:ro
    command: ["ocserv", "-c", "/etc/ocserv/ocserv.conf", "-f"]
    networks: [wan, inner_b]
  whoami-b:
    image: traefik/whoami:latest
    command: ["--port=80"]
    networks:
      inner_b:
        ipv4_address: 192.168.91.10

networks:
  wan:
  inner_a:
    internal: true
    ipam: { config: [{ subnet: 192.168.90.0/24 }] }
  inner_b:
    internal: true
    ipam: { config: [{ subnet: 192.168.91.0/24 }] }
```
Примечание: образ ocserv может отличаться директивами/путями конфига — если `tiredofit/ocserv` не подойдёт, в шаге 5 фиксируем фактический образ и правим пути. Это ожидаемая точка подгонки.

- [ ] **Step 4: Скрипты up/down**

Создать `test/e2e/backends/ocserv/up.sh`:
```bash
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
./gen-certs.sh
# passwd для логина user/pass (ocpasswd-формат генерим через контейнер)
if [[ ! -f passwd ]]; then
  docker run --rm -e PASS=pass tiredofit/ocserv:latest \
    sh -c 'echo "$PASS" | ocpasswd -c /dev/stdout user' > passwd 2>/dev/null || \
    printf 'user:lab.local:$1$xxLABxx$placeholder\n' > passwd
fi
docker compose up -d
echo "ждём готовности ocserv..."
for i in $(seq 1 30); do
  if openssl s_client -connect 127.0.0.1:4443 -servername localhost </dev/null 2>/dev/null | grep -q CONNECTED; then
    echo "ocserv-a отвечает на :4443"; break
  fi
  sleep 1
done
```
Создать `test/e2e/backends/ocserv/down.sh`:
```bash
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
docker compose down -v || true
```
Сделать исполняемыми: `chmod +x up.sh down.sh`.

Примечание по `passwd`: точная генерация ocpasswd-хэша зависит от образа; если строка выше не даст рабочий хэш, в шаге 5 сгенерировать хэш внутри запущенного контейнера (`docker compose exec ocserv-a ocpasswd -c /etc/ocserv/passwd user`) и перемонтировать. Зафиксировать рабочий способ.

- [ ] **Step 5: Поднять и проверить эталоном `openconnect`**

Run:
```bash
cd /home/ub/dualvpn/test/e2e/backends/ocserv && ./up.sh
docker compose ps
```
Expected: оба `ocserv-*` в статусе `Up`, `:4443`/`:4444` слушаются.

Затем эталонная проверка (штатный клиент, доказывает что сервер жив):
```bash
echo pass | sudo openconnect --protocol=anyconnect --servercert=pin-sha256:$(:) \
  -u user --passwd-on-stdin --non-inter --no-dtls 127.0.0.1:4443 2>&1 | head -20 || true
```
Expected: `openconnect` проходит аутентификацию и получает `10.90.0.x` (либо явная причина отказа). Записать фактический результат (в т.ч. как именно указывать servercert/CA) в журнал расхождений спеки.

- [ ] **Step 6: .gitignore и commit**

В `.gitignore` добавить:
```
# E2E-стенд: генерируемые артефакты
test/e2e/backends/ocserv/certs/
test/e2e/backends/ocserv/passwd
bin/dualvpn-harness
```
Commit:
```bash
cd /home/ub/dualvpn
git add test/e2e/backends/ocserv/gen-certs.sh test/e2e/backends/ocserv/ocserv-a.conf \
  test/e2e/backends/ocserv/ocserv-b.conf test/e2e/backends/ocserv/docker-compose.yml \
  test/e2e/backends/ocserv/up.sh test/e2e/backends/ocserv/down.sh .gitignore
git commit -m "test(e2e): бэкенд ocserv (compose, серты, конфиги, up/down) + эталон openconnect"
```

---

### Task 6: Оркестрация `run.sh` + `make e2e`

**Files:**
- Create: `test/e2e/run.sh`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `bin/dualvpn-harness` (Task 3), бэкенд ocserv (Task 5).
- Produces: `make e2e` — сборка харнесса, подъём ocserv, прогон SOCKS5 (без root) и TUN (через sudo), curl-проверка изоляции, teardown.

- [ ] **Step 1: Конфиг стенда для ocserv**

Создать `test/e2e/backends/ocserv/config.toml`:
```toml
[mode]
  preferred = "socks5"

[[tunnels]]
  name = "a"
  endpoint = "127.0.0.1:4443"
  group = "LAB"
  socks_port = 21080
  tun_name = "dvlab0"
  routes = ["192.168.90.0/24"]
  username = "user"
  password = "pass"
  probe_url = "http://192.168.90.10/"

[[tunnels]]
  name = "b"
  endpoint = "127.0.0.1:4444"
  group = "LAB"
  socks_port = 21081
  tun_name = "dvlab1"
  routes = ["192.168.91.0/24"]
  username = "user"
  password = "pass"
  probe_url = "http://192.168.91.10/"
```

- [ ] **Step 2: run.sh**

Создать `test/e2e/run.sh`:
```bash
#!/usr/bin/env bash
# E2E host-прогон: ocserv-бэкенд + харнесс (SOCKS5, затем TUN).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BACKEND="${E2E_BACKEND:-ocserv}"
OCS="$ROOT/test/e2e/backends/ocserv"
CFG="$OCS/config.toml"
export PATH="/usr/local/go/bin:$PATH"

cleanup() { "$OCS/down.sh" || true; }
trap cleanup EXIT

echo "==> сборка харнесса"
go build -o "$ROOT/bin/dualvpn-harness" "$ROOT/cmd/dualvpn-harness/"

echo "==> подъём бэкенда $BACKEND"
"$OCS/up.sh"

echo "==> SOCKS5-прогон (без root)"
"$ROOT/bin/dualvpn-harness" -config "$CFG" -mode socks5 -insecure -timeout 30s
sock_rc=$?
echo "SOCKS5 exit=$sock_rc"

echo "==> проверка изоляции через curl --socks5"
if curl -s --max-time 5 --socks5 127.0.0.1:21080 http://192.168.91.10/ >/dev/null; then
  echo "FAIL: туннель A достаёт до сети B (изоляция нарушена)"; exit 1
else
  echo "OK: сеть B недоступна через туннель A"
fi

echo "==> TUN-прогон (через sudo)"
sudo -E "$ROOT/bin/dualvpn-harness" -config "$CFG" -mode tun -insecure -timeout 30s
tun_rc=$?
echo "TUN exit=$tun_rc"

echo "==> ИТОГ: socks=$sock_rc tun=$tun_rc"
exit $(( sock_rc + tun_rc ))
```
Сделать исполняемым: `chmod +x test/e2e/run.sh`.

- [ ] **Step 3: Makefile-цель**

В `Makefile` добавить:
```makefile
.PHONY: e2e
e2e: ## E2E host-стенд: ocserv + харнесс (SOCKS5 + TUN)
	@test/e2e/run.sh
```

- [ ] **Step 4: Прогнать mockasa-milestone (не требует docker) как smoke `make`-пути**

Run:
```bash
export PATH="/usr/local/go/bin:$PATH"
cd /home/ub/dualvpn && go test ./test/e2e/ -run TestDualTunnelSocksIsolation -count=1 -timeout 60s 2>&1 | tail -5
```
Expected: `PASS` — база стенда зелёная независимо от ocserv.

- [ ] **Step 5: Прогнать полный ocserv-путь**

Run:
```bash
cd /home/ub/dualvpn && make e2e 2>&1 | tail -40
```
Expected один из двух зафиксированных исходов:
- **PASS** — DualVPN сошёлся с ocserv, обе проверки/изоляция прошли (`exit 0`); ИЛИ
- **Документированная находка** — харнесс не проходит handshake с ocserv, при этом эталонный `openconnect` (Task 5, шаг 5) сервер принимает. В этом случае занести в спеку («Журнал расхождений») точное место расхождения (лог `[a] error: ...`) — это ожидаемый результат лестницы совместимости, а не провал стенда. Milestone-тест (шаг 4) при этом обязан оставаться зелёным.

- [ ] **Step 6: Commit**

```bash
cd /home/ub/dualvpn
git add test/e2e/run.sh test/e2e/backends/ocserv/config.toml Makefile
git commit -m "test(e2e): оркестрация make e2e (ocserv + харнесс SOCKS5/TUN) + проверка изоляции"
```

---

## Self-Review

**Покрытие спеки (Linux-ядро, host-прогон):**
- Пункт «два туннеля одновременно + изоляция» → Task 4 (mockasa, детерминированно) + Task 6 (ocserv, curl).
- «TUN-режим (реальные маршруты)» → Task 6 шаг 5 (sudo, `-mode tun`).
- «SOCKS5-режим (без прав)» → Task 4 + Task 6 шаг 2.
- «Подключаемый бэкенд» → `mockasa` (Task 4) и `ocserv` (Task 5) за одинаковым интерфейсом харнесса; `E2E_BACKEND` в run.sh.
- «Лестница совместимости» → Task 5 шаг 5 (эталон openconnect) + Task 6 шаг 5 (харнесс vs ocserv, находка в журнал).
- «Переиспользование боевого vpn.Manager» → Task 3/4 (`harness.Run` поверх `vpn.Manager`).
- **Вне рамок этого плана** (следующий план): клиентская Linux-VM (cloud-init/qemu, bridge/tap, `.deb`, GUI-smoke под Xvfb), Windows-VM, asav-бэкенд. GUI-smoke и проверка `.deb` относятся к VM-слою и здесь не выполняются — отмечено явно.

**Плейсхолдеры:** боевого кода без реализации нет. Три «точки подгонки» (образ ocserv, генерация ocpasswd-хэша, формат servercert для openconnect) помечены как ожидаемые и локализованы в Task 5 — они зависят от внешнего образа и проверяются эмпирически на шаге 5, а не угадываются.

**Согласованность типов:** `harness.Run/Options/GetWithRetry/BuildConfigs` (экспорт из `test/e2e/harness` после Task 4 шаг 2) используются одинаково в `cmd/dualvpn-harness/main.go` и `test/e2e/mockasa_e2e_test.go`. `config.Tunnel.ProbeURL` добавлено в Task 3 и используется в Task 4/6. `sslcon.ModeSOCKS5/ModeTUN` — существующие константы.

## Следующий план (после этого)

`docs/superpowers/plans/…-dualvpn-e2e-linux-vm.md`: клиентская Linux-VM (Ubuntu cloud-init + qemu), сеть `br-dualvpn`+tap, установка `dualvpn_*.deb` на чистой системе, прогон харнесса внутри VM (TUN+SOCKS5) и GUI-smoke `xvfb-run dualvpn`. Затем — Windows-VM (autounattend) и опциональный asav-бэкенд.
