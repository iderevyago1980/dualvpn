package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// callTimeout — ожидание ответа службы. Операции подключения возвращаются
// сразу (результат приходит событиями), поэтому запрос не должен висеть.
const callTimeout = 30 * time.Second

// Client — соединение приложения со службой.
type Client struct {
	rwc io.ReadWriteCloser

	nextID atomic.Uint64
	events chan Event

	mu      sync.Mutex
	pending map[uint64]chan Response
	closed  bool
	readErr error
}

// NewClient оборачивает уже открытое соединение (именованный канал в бою,
// net.Pipe в тестах) и запускает чтение кадров.
func NewClient(rwc io.ReadWriteCloser) *Client {
	c := &Client{
		rwc:     rwc,
		events:  make(chan Event, 64),
		pending: make(map[uint64]chan Response),
	}
	go c.readLoop()
	return c
}

// Events возвращает канал событий туннелей. Закрывается при обрыве связи —
// приложение по этому признаку понимает, что служба недоступна.
func (c *Client) Events() <-chan Event { return c.events }

// Close закрывает соединение со службой.
func (c *Client) Close() error { return c.rwc.Close() }

// readLoop разбирает кадры: ответы раздаёт ожидающим вызовам, события
// кладёт в канал.
func (c *Client) readLoop() {
	sc := bufio.NewScanner(c.rwc)
	sc.Buffer(make([]byte, 0, 8*1024), 1024*1024)

	for sc.Scan() {
		var f Frame
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			continue // мусорный кадр пропускаем: связь может продолжаться
		}
		switch {
		case f.Event != nil:
			select {
			case c.events <- *f.Event:
			default: // потребитель отстал — событие статуса можно потерять
			}
		case f.Response != nil:
			c.deliver(*f.Response)
		}
	}

	err := sc.Err()
	if err == nil {
		err = errors.New("соединение со службой закрыто")
	}
	c.fail(err)
}

// deliver отдаёт ответ ожидающему вызову.
func (c *Client) deliver(resp Response) {
	c.mu.Lock()
	ch, ok := c.pending[resp.ID]
	delete(c.pending, resp.ID)
	c.mu.Unlock()
	if ok {
		ch <- resp
	}
}

// fail будит все ожидающие вызовы при обрыве связи.
func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.readErr = err
	pending := c.pending
	c.pending = make(map[uint64]chan Response)
	c.mu.Unlock()

	for id, ch := range pending {
		ch <- Response{ID: id, Error: err.Error()}
	}
	close(c.events)
}

// call отправляет запрос и ждёт ответ.
func (c *Client) call(method string, params any, result any) error {
	var raw json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = data
	}

	id := c.nextID.Add(1)
	ch := make(chan Response, 1)

	c.mu.Lock()
	if c.closed {
		err := c.readErr
		c.mu.Unlock()
		return err
	}
	c.pending[id] = ch
	c.mu.Unlock()

	data, err := json.Marshal(Request{ID: id, Method: method, Params: raw})
	if err != nil {
		return err
	}
	if _, err := c.rwc.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("служба недоступна: %w", err)
	}

	select {
	case resp := <-ch:
		if resp.Error != "" {
			return errors.New(resp.Error)
		}
		if result != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	case <-time.After(callTimeout):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return errors.New("служба не ответила вовремя")
	}
}

// Version возвращает версию службы.
func (c *Client) Version() (string, error) {
	var v string
	err := c.call(MethodVersion, nil, &v)
	return v, err
}

// Status возвращает состояние туннелей службы.
func (c *Client) Status() ([]TunnelState, error) {
	var st []TunnelState
	err := c.call(MethodStatus, nil, &st)
	return st, err
}

// Connect просит службу поднять туннель.
func (c *Client) Connect(p ConnectParams) error { return c.call(MethodConnect, p, nil) }

// Disconnect останавливает туннель.
func (c *Client) Disconnect(id string) error { return c.call(MethodDisconnect, IDParams{ID: id}, nil) }

// DisconnectAll останавливает все туннели службы.
func (c *Client) DisconnectAll() error { return c.call(MethodDisconnectAll, nil, nil) }

// Submit2FA передаёт код второго фактора.
func (c *Client) Submit2FA(id, code string) error {
	return c.call(MethodSubmit2FA, TwoFAParams{ID: id, Code: code}, nil)
}
