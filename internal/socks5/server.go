// Package socks5 — локальный SOCKS5-прокси для режима работы без админ-прав.
//
// ВНИМАНИЕ: в текущем SOCKS5-режиме этот пакет НЕ используется — SOCKS5-сервер
// поднимает ocproxy, запускаемый самим openconnect (--script-tun --script
// 'ocproxy -D <port>'). Пакет оставлен для будущего gVisor-режима на Windows,
// где ocproxy недоступен и SOCKS5 придётся реализовать поверх netstack.
//
// Каждый VPN-туннель поднимает свой SOCKS5-сервер на отдельном порту;
// приложения направляют трафик через него (см. SPEC.md, «SOCKS5-режим»).
package socks5

import (
	"context"
	"fmt"
	"net"
	"sync"

	gosocks5 "github.com/armon/go-socks5"
)

// DialFunc — функция установки исходящего соединения. В SOCKS5-режиме сюда
// подставляется dialer gVisor netstack, направляющий трафик в VPN-туннель.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Server — SOCKS5-сервер одного туннеля.
type Server struct {
	port  int
	inner *gosocks5.Server

	mu       sync.Mutex // защищает listener: Stop может прийти конкурентно
	listener net.Listener
}

// New создаёт SOCKS5-сервер на указанном локальном порту.
// Если dial == nil, соединения устанавливаются напрямую (без туннеля) —
// полезно для отладки каркаса до интеграции netstack.
func New(port int, dial DialFunc) (*Server, error) {
	conf := &gosocks5.Config{}
	if dial != nil {
		conf.Dial = dial
	}
	inner, err := gosocks5.New(conf)
	if err != nil {
		return nil, fmt.Errorf("создание SOCKS5-сервера: %w", err)
	}
	return &Server{port: port, inner: inner}, nil
}

// Addr возвращает локальный адрес прослушивания.
func (s *Server) Addr() string {
	return fmt.Sprintf("127.0.0.1:%d", s.port)
}

// Start начинает принимать SOCKS5-соединения. Не блокирует:
// обслуживание идёт в отдельной горутине до вызова Stop.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.Addr())
	if err != nil {
		return fmt.Errorf("прослушивание %s: %w", s.Addr(), err)
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()
	go s.inner.Serve(ln) //nolint:errcheck // Serve возвращает ошибку при закрытии listener
	return nil
}

// Stop останавливает сервер, закрывая listener. Потокобезопасен и
// идемпотентен: может вызываться одновременно из Bridge.Close и
// горутины отмены контекста.
func (s *Server) Stop() error {
	s.mu.Lock()
	ln := s.listener
	s.listener = nil
	s.mu.Unlock()
	if ln == nil {
		return nil
	}
	return ln.Close()
}
