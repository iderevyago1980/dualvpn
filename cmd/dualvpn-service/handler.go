package main

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"dualvpn/internal/ipc"
	"dualvpn/internal/vpn"
	"dualvpn/internal/vpn/sslcon"
)

// version — версия службы; подставляется линкером, как и версия приложения.
var version = "dev"

// handler исполняет запросы приложения на боевом менеджере туннелей.
// Туннели всегда поднимаются в режиме TUN: ради него служба и нужна —
// SOCKS5 приложение умеет само, без прав администратора.
type handler struct {
	ctx context.Context
	mgr *vpn.Manager

	mu    sync.Mutex
	known map[string]struct{} // туннели, зарегистрированные в менеджере
}

func newHandler(ctx context.Context, mgr *vpn.Manager) *handler {
	return &handler{ctx: ctx, mgr: mgr, known: make(map[string]struct{})}
}

// Connect регистрирует туннель и запускает его. Повторный вызов для уже
// поднятого туннеля возвращает ошибку менеджера — приложение покажет её
// пользователю.
func (h *handler) Connect(p ipc.ConnectParams) error {
	h.mgr.AddTunnel(vpn.TunnelConfig{
		ID: p.ID,
		Opts: sslcon.ClientConfig{
			Host:     p.Host,
			Group:    p.Group,
			Username: p.Username,
			Password: p.Password,
			TunName:  p.TunName,
		},
		Routes: p.Routes,
		Mode:   sslcon.ModeTUN,
	})

	h.mu.Lock()
	h.known[p.ID] = struct{}{}
	h.mu.Unlock()

	if err := h.mgr.Start(h.ctx, p.ID); err != nil {
		return fmt.Errorf("туннель %q: %w", p.ID, err)
	}
	return nil
}

// Disconnect останавливает туннель.
func (h *handler) Disconnect(id string) error { return h.mgr.Stop(id) }

// DisconnectAll останавливает все туннели службы.
func (h *handler) DisconnectAll() error {
	h.mgr.StopAll()
	return nil
}

// Submit2FA передаёт код второго фактора туннелю, запросившему его.
func (h *handler) Submit2FA(id, code string) error { return h.mgr.Submit2FA(id, code) }

// Status возвращает состояние известных службе туннелей.
func (h *handler) Status() []ipc.TunnelState {
	h.mu.Lock()
	ids := make([]string, 0, len(h.known))
	for id := range h.known {
		ids = append(ids, id)
	}
	h.mu.Unlock()

	// Порядок карты случаен — фиксируем, чтобы список не «дрожал» в интерфейсе.
	sort.Strings(ids)

	states := make([]ipc.TunnelState, 0, len(ids))
	for _, id := range ids {
		connected, _ := h.mgr.Status(id)
		states = append(states, ipc.TunnelState{ID: id, Connected: connected})
	}
	return states
}

// Version возвращает версию службы: приложение сверяет её со своей.
func (h *handler) Version() string { return version }

// forwardEvents пересылает события менеджера всем подключённым клиентам.
// Завершается вместе с каналом менеджера.
func forwardEvents(mgr *vpn.Manager, srv *ipc.Server) {
	for ev := range mgr.Events() {
		srv.Broadcast(ipc.Event{
			TunnelID: ev.TunnelID,
			Type:     string(ev.Event.Type),
			Message:  ev.Event.Message,
		})
	}
}
