// Package main (dualvpn-harness) — headless-драйвер стенда: поднимает
// туннели через боевой vpn.Manager без Wails.
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
