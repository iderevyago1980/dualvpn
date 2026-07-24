// Package harness — переиспользуемый драйвер стенда: поднимает туннели
// через боевой vpn.Manager без Wails. Используется и headless-бинарём
// cmd/dualvpn-harness, и E2E-тестами (test/e2e).
package harness

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"dualvpn/internal/config"
	"dualvpn/internal/vpn"
	"dualvpn/internal/vpn/sslcon"
	"dualvpn/test/e2e/checks"
)

// BuildConfigs зеркалит ui.App.registerTunnels, но режим и insecure задаются
// стендом (не автодетекцией). ID туннеля = имя из конфига.
func BuildConfigs(cfg *config.Config, mode string, insecure bool) []vpn.TunnelConfig {
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

// Options — параметры запуска харнесса.
type Options struct {
	Cfg          *config.Config
	Mode         string
	Insecure     bool
	ReadyTimeout time.Duration
	Logf         func(string, ...any)
}

// Run регистрирует туннели, запускает их и ждёт готовности всех.
// Проверки связности выполняет вызывающий (они зависят от бэкенда).
// Останавливать менеджер — тоже вызывающему: defer m.StopAll().
func Run(ctx context.Context, opts Options) (*vpn.Manager, []string, error) {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	cfgs := BuildConfigs(opts.Cfg, opts.Mode, opts.Insecure)
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

// GetWithRetry повторяет GET, пока не получит 200 либо не исчерпает попытки:
// SOCKS5-мост становится готов чуть позже события Connected.
func GetWithRetry(client *http.Client, url string, attempts int, pause time.Duration) (int, string, error) {
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
