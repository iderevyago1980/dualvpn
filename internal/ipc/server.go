package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
)

// Handler — операции, которые служба выполняет по запросу приложения.
// Реализация живёт в службе и работает с боевым vpn.Manager.
type Handler interface {
	Connect(p ConnectParams) error
	Disconnect(id string) error
	DisconnectAll() error
	Submit2FA(id, code string) error
	Status() []TunnelState
	Version() string
}

// Server принимает подключения приложения и рассылает события туннелей
// всем подключённым клиентам.
type Server struct {
	h Handler

	// Logf — необязательный журнал службы: сюда пишется, почему оборвалось
	// соединение с приложением. Без него такие обрывы приходится
	// расследовать по косвенным признакам.
	Logf func(string, ...any)

	mu      sync.Mutex
	clients map[*conn]struct{}
}

// NewServer создаёт сервер поверх обработчика.
func NewServer(h Handler) *Server {
	return &Server{h: h, clients: make(map[*conn]struct{})}
}

// logf пишет в журнал службы, если он задан.
func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// conn — одно клиентское подключение с сериализацией записи: кадры пишут
// и цикл ответов, и рассылка событий.
type conn struct {
	mu sync.Mutex
	w  io.Writer
}

func (c *conn) send(f Frame) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.w.Write(append(data, '\n'))
	return err
}

// Serve обслуживает подключения до закрытия слушателя.
func (s *Server) Serve(ln net.Listener) error {
	for {
		nc, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.handleConn(nc)
	}
}

// Broadcast рассылает событие всем подключённым клиентам. Ошибки записи
// игнорируются: отвалившийся клиент отцепится своим циклом чтения.
func (s *Server) Broadcast(ev Event) {
	s.mu.Lock()
	clients := make([]*conn, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()

	frame := Frame{Event: &ev}
	for _, c := range clients {
		_ = c.send(frame)
	}
}

// handleConn читает запросы клиента до закрытия соединения.
func (s *Server) handleConn(nc net.Conn) {
	defer nc.Close() //nolint:errcheck // соединение закрывается при выходе

	c := &conn{w: nc}
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
	}()

	// Ограничение размера строки защищает от клиента, шлющего бесконечный
	// кадр: служба привилегированная, память ей тратить впустую нельзя.
	sc := bufio.NewScanner(nc)
	sc.Buffer(make([]byte, 0, 8*1024), 1024*1024)

	for sc.Scan() {
		var req Request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			_ = c.send(Frame{Response: &Response{Error: "неразборчивый запрос: " + err.Error()}})
			continue
		}
		resp := s.dispatch(req)
		if err := c.send(Frame{Response: &resp}); err != nil {
			s.logf("не удалось отправить ответ на %q: %v", req.Method, err)
			return
		}
	}
	if err := sc.Err(); err != nil {
		s.logf("чтение из канала прервано: %v", err)
	}
}

// dispatch исполняет запрос и формирует ответ. Параметры проверяются до
// исполнения: запрос приходит от непривилегированного процесса.
func (s *Server) dispatch(req Request) Response {
	resp := Response{ID: req.ID}
	fail := func(err error) Response {
		resp.Error = err.Error()
		return resp
	}

	switch req.Method {
	case MethodVersion:
		return withResult(resp, s.h.Version())

	case MethodStatus:
		return withResult(resp, s.h.Status())

	case MethodConnect:
		var p ConnectParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return fail(err)
		}
		if err := p.Validate(); err != nil {
			return fail(err)
		}
		if err := s.h.Connect(p); err != nil {
			return fail(err)
		}
		return resp

	case MethodDisconnect:
		var p IDParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return fail(err)
		}
		if err := p.Validate(); err != nil {
			return fail(err)
		}
		if err := s.h.Disconnect(p.ID); err != nil {
			return fail(err)
		}
		return resp

	case MethodDisconnectAll:
		if err := s.h.DisconnectAll(); err != nil {
			return fail(err)
		}
		return resp

	case MethodSubmit2FA:
		var p TwoFAParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return fail(err)
		}
		if err := p.Validate(); err != nil {
			return fail(err)
		}
		if err := s.h.Submit2FA(p.ID, p.Code); err != nil {
			return fail(err)
		}
		return resp
	}

	return fail(errors.New("неизвестный метод " + req.Method))
}

// withResult кладёт значение в ответ, превращая ошибку сериализации в
// ошибку ответа.
func withResult(resp Response, v any) Response {
	data, err := json.Marshal(v)
	if err != nil {
		resp.Error = err.Error()
		return resp
	}
	resp.Result = data
	return resp
}
