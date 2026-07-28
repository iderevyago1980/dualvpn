package pac

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// path — путь, по которому браузер забирает скрипт.
const path = "/proxy.pac"

// Server раздаёт PAC-скрипт по HTTP на локальном интерфейсе.
// Слушает только 127.0.0.1: настройка прокси — локальное дело машины,
// отдавать её в сеть незачем.
type Server struct {
	mu       sync.RWMutex
	script   string
	listener net.Listener
	srv      *http.Server
}

// NewServer создаёт сервер с пустым набором правил (всё — DIRECT).
func NewServer() *Server {
	s := &Server{script: Script(nil)}
	mux := http.NewServeMux()
	mux.HandleFunc(path, s.handle)
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return s
}

// Start начинает слушать на 127.0.0.1:port. Порт 0 — выбрать свободный
// (фактический адрес возвращает URL). Повторный вызов — ошибка.
func (s *Server) Start(port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return errors.New("pac: сервер уже запущен")
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("pac: прослушивание порта %d: %w", port, err)
	}
	s.listener = ln
	go s.srv.Serve(ln) //nolint:errcheck // Serve возвращает ошибку при закрытии listener
	return nil
}

// SetTunnels пересобирает скрипт под текущий набор туннелей.
// Безопасен для вызова в любой момент: браузер перечитывает PAC сам.
func (s *Server) SetTunnels(tunnels []Tunnel) {
	script := Script(tunnels)
	s.mu.Lock()
	s.script = script
	s.mu.Unlock()
}

// URL возвращает адрес PAC-файла для браузера ("" — сервер не запущен).
func (s *Server) URL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.listener == nil {
		return ""
	}
	return "http://" + s.listener.Addr().String() + path
}

// Script возвращает текущий текст скрипта (для журнала и тестов).
func (s *Server) Script() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.script
}

// Close останавливает сервер. Идемпотентен.
func (s *Server) Close() error {
	s.mu.Lock()
	ln := s.listener
	s.listener = nil
	s.mu.Unlock()
	if ln == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

func (s *Server) handle(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	script := s.script
	s.mu.RUnlock()

	// Тип содержимого обязателен: браузеры игнорируют PAC с чужим MIME.
	w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
	// Правила меняются при подключении и отключении туннелей — кэшировать нельзя.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(script))
}
