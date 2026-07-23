// Package vpn — обёртка над CLI-клиентом openconnect (Cisco AnyConnect SSL/TLS).
//
// Клиент запускается как subprocess; пароль и 2FA-код передаются через stdin
// в ответ на интерактивные промпты, вывод парсится для определения статуса.
package vpn

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// EventType — тип события жизненного цикла туннеля.
type EventType string

const (
	EventConnected    EventType = "connected"    // Туннель установлен
	EventDisconnected EventType = "disconnected" // Процесс openconnect завершился
	EventError        EventType = "error"        // Ошибка аутентификации/подключения
	Event2FARequired  EventType = "2fa_required" // Сервер запросил второй фактор (TOTP)
)

// Event — событие от процесса openconnect.
type Event struct {
	Type    EventType
	Message string // Человекочитаемое описание (строка вывода openconnect)
}

// Options — параметры запуска openconnect для одного туннеля.
type Options struct {
	Binary   string   // Путь к бинарнику openconnect (по умолчанию /usr/sbin/openconnect)
	Server   string   // Адрес VPN-сервера (например, vpn2.astralinux.ru)
	Group    string   // Tunnel-group на ASA (--usergroup)
	Username string   // Логин (--user)
	Password string   // Пароль; отправляется в stdin на промпт "Password:"
	Script   string   // Опциональный vpnc-script (например, ocproxy для SOCKS5-режима)
	ExtraArgs []string // Дополнительные аргументы CLI
}

// Client — управляет одним процессом openconnect.
type Client struct {
	opts   Options
	events chan Event
	twoFA  chan string // Канал для передачи 2FA-кода от UI/пользователя

	mu   sync.Mutex
	cmd  *exec.Cmd
	stdin io.WriteCloser
}

// New создаёт клиент с указанными параметрами.
func New(opts Options) *Client {
	if opts.Binary == "" {
		opts.Binary = "/usr/sbin/openconnect"
	}
	return &Client{
		opts:   opts,
		events: make(chan Event, 16),
		twoFA:  make(chan string, 1),
	}
}

// Events возвращает канал событий туннеля. Канал закрывается после
// завершения процесса openconnect.
func (c *Client) Events() <-chan Event {
	return c.events
}

// Submit2FA передаёт 2FA-код (TOTP), запрошенный сервером.
// Вызывается после получения события Event2FARequired.
func (c *Client) Submit2FA(code string) {
	select {
	case c.twoFA <- code:
	default: // код уже ожидает отправки — не блокируемся
	}
}

// Start запускает openconnect и начинает разбор его вывода.
// Метод не блокирует: статус приходит через канал Events().
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil {
		return fmt.Errorf("туннель %s уже запущен", c.opts.Server)
	}

	// Интерактивный режим (промпты пароля/2FA) у openconnect включён по умолчанию.
	args := []string{
		"--protocol=anyconnect",
		"--user=" + c.opts.Username,
		"--servercert=accept", // принять серверный сертификат при первом подключении
	}
	if c.opts.Group != "" {
		args = append(args, "--usergroup="+c.opts.Group)
	}
	if c.opts.Script != "" {
		args = append(args, "--script="+c.opts.Script)
	}
	args = append(args, c.opts.ExtraArgs...)
	args = append(args, c.opts.Server) // сервер передаётся позиционным аргументом

	cmd := exec.CommandContext(ctx, c.opts.Binary, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	// Промпты openconnect пишет в stderr — объединяем со stdout для разбора.
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("запуск %s: %w", c.opts.Binary, err)
	}
	c.cmd = cmd
	c.stdin = stdin

	go c.readLoop(stdout)
	go c.waitLoop()
	return nil
}

// Stop корректно завершает openconnect (SIGINT → openconnect сам снимает маршруты).
func (c *Client) Stop() error {
	c.mu.Lock()
	cmd := c.cmd
	c.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		return cmd.Process.Kill()
	}
	// Даём процессу время на graceful shutdown, затем убиваем.
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }() //nolint:errcheck // ошибка выхода не важна при остановке
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		return cmd.Process.Kill()
	}
	return nil
}

// readLoop читает объединённый stdout/stderr openconnect, распознаёт промпты
// (которые не заканчиваются переводом строки) и строки статуса.
func (c *Client) readLoop(r io.Reader) {
	br := bufio.NewReader(r)
	var buf strings.Builder

	flush := func() {
		line := strings.TrimSpace(buf.String())
		buf.Reset()
		if line != "" {
			c.handleLine(line)
		}
	}

	for {
		b, err := br.ReadByte()
		if err != nil {
			flush()
			return
		}
		if b == '\n' {
			flush()
			continue
		}
		buf.WriteByte(b)
		// Промпт вида "Password:" или "Two-factor token:" не завершается \n —
		// проверяем накопленный буфер по суффиксу.
		if b == ':' {
			c.maybePrompt(buf.String())
		}
	}
}

// maybePrompt отвечает на интерактивные промпты openconnect.
// Порядок при 2FA: сначала "Password:", затем "Two-factor token:" (или "Response:").
func (c *Client) maybePrompt(text string) {
	t := strings.ToLower(strings.TrimSpace(text))
	switch {
	case strings.HasSuffix(t, "password:"):
		c.writeLine(c.opts.Password)
	case strings.HasSuffix(t, "two-factor token:"),
		strings.HasSuffix(t, "response:"),
		strings.HasSuffix(t, "verification code:"),
		strings.HasSuffix(t, "second password:"):
		c.emit(Event2FARequired, strings.TrimSpace(text))
		// Блокируемся до получения кода от пользователя (Submit2FA).
		code := <-c.twoFA
		c.writeLine(code)
	case strings.HasSuffix(t, "username:"):
		c.writeLine(c.opts.Username)
	}
}

// handleLine разбирает завершённую строку вывода и генерирует события статуса.
func (c *Client) handleLine(line string) {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "connected as"), // "Connected as 10.x.x.x, using SSL"
		strings.Contains(l, "established dtls connection"),
		strings.Contains(l, "session authentication will expire"):
		c.emit(EventConnected, line)
	case strings.Contains(l, "login failed"),
		strings.Contains(l, "authentication failed"),
		strings.Contains(l, "certificate verification failed"),
		strings.Contains(l, "failed to connect"),
		strings.Contains(l, "fgets (stdin)"): // не смогли ответить на промпт
		c.emit(EventError, line)
	}
}

// waitLoop ждёт завершения процесса и закрывает канал событий.
func (c *Client) waitLoop() {
	c.mu.Lock()
	cmd := c.cmd
	c.mu.Unlock()

	err := cmd.Wait()
	msg := "openconnect завершился"
	if err != nil {
		msg = fmt.Sprintf("openconnect завершился с ошибкой: %v", err)
	}
	c.emit(EventDisconnected, msg)

	c.mu.Lock()
	c.cmd = nil
	c.stdin = nil
	c.mu.Unlock()
	close(c.events)
}

// writeLine отправляет строку (пароль/код) в stdin процесса.
func (c *Client) writeLine(s string) {
	c.mu.Lock()
	stdin := c.stdin
	c.mu.Unlock()
	if stdin != nil {
		fmt.Fprintln(stdin, s) //nolint:errcheck // разрыв pipe обнаружится в readLoop
	}
}

// emit кладёт событие в канал, не блокируясь при переполнении буфера.
func (c *Client) emit(t EventType, msg string) {
	select {
	case c.events <- Event{Type: t, Message: msg}:
	default: // потребитель отстал — событие статуса можно потерять
	}
}
