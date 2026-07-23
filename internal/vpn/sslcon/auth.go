// Форк sslcon/auth/auth.go: package-level глобалы (Prof, Conn, BufR,
// WebVpnCookie, reqHeaders) заменены на поля структуры Client — это
// позволяет держать несколько туннелей одновременно.
package sslcon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"text/template"
	"time"

	"sslcon/base"
	"sslcon/proto"
	"sslcon/utils"
)

// ClientConfig — конфигурация одного туннеля.
type ClientConfig struct {
	Host               string
	Username           string
	Password           string
	Group              string
	SecretKey          string
	InsecureSkipVerify bool
}

// Validate проверяет обязательные поля конфигурации.
func (cfg ClientConfig) Validate() error {
	if cfg.Host == "" {
		return errors.New("sslcon: config: не указан host")
	}
	if cfg.Username == "" {
		return errors.New("sslcon: config: не указан username")
	}
	if cfg.Password == "" {
		return errors.New("sslcon: config: не указан password")
	}
	return nil
}

// Client — состояние аутентификации одного туннеля.
// Бывшие глобалы sslcon/auth стали полями; методы повторяют логику
// auth.InitAuth / auth.PasswordAuth / tplPost.
type Client struct {
	Prof         *Profile
	Conn         *tls.Conn
	BufR         *bufio.Reader
	WebVpnCookie string
	SessionToken string

	insecureSkipVerify bool
	mu                 sync.Mutex

	// Поля для high-level API (совместимость с openconnect.Client).
	events chan Event
	twoFA  chan string
	tunnel *Tunnel
	running bool
}

// EventType — тип события жизненного цикла туннеля.
// Совместим с vpn.EventType из openconnect-обёртки.
type EventType string

const (
	EventConnected    EventType = "connected"
	EventDisconnected EventType = "disconnected"
	EventError        EventType = "error"
	Event2FARequired  EventType = "2fa_required"
)

// Event — событие от sslcon-клиента.
type Event struct {
	Type    EventType
	Message string
}

// Типы XML-шаблонов (как в sslcon/auth).
const (
	tplInit = iota
	tplAuthReply
)

// sslcon/base хранит глобальный конфиг и логгер — инициализируем один раз
// на процесс, иначе base.Debug паникует (baseLogger == nil). Глобальность
// base безопасна: per-tunnel состояние вынесено в Client.
var baseSetupOnce sync.Once

func ensureBase() {
	baseSetupOnce.Do(func() {
		if base.GetBaseLogger() == nil {
			base.Setup()
		}
	})
}

// NewClient создаёт клиент аутентификации для одного туннеля.
func NewClient(cfg ClientConfig) *Client {
	ensureBase()
	return &Client{
		Prof:               NewProfile(cfg.Host, cfg.Username, cfg.Password, cfg.Group, cfg.SecretKey),
		insecureSkipVerify: cfg.InsecureSkipVerify,
	}
}

// InitAuth устанавливает TLS-соединение, определяет группу пользователя и
// адрес аутентификации AuthPath. Аналог auth.InitAuth, но на состоянии Client.
func (c *Client) InitAuth() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.WebVpnCookie = ""
	config := tls.Config{
		InsecureSkipVerify: c.insecureSkipVerify,
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 6 * time.Second}, "tcp4", c.Prof.HostWithPort, &config)
	if err != nil {
		return err
	}
	c.Conn = conn
	c.BufR = bufio.NewReader(conn)

	dtd := new(proto.DTD)

	c.Prof.AppVersion = base.Cfg.AgentVersion
	c.Prof.MacAddress = base.LocalInterface.Mac

	err = c.tplPost(tplInit, "", dtd)
	if err != nil {
		return err
	}
	c.Prof.AuthPath = dtd.Auth.Form.Action
	if c.Prof.AuthPath == "" {
		c.Prof.AuthPath = "/"
	}
	c.Prof.TunnelGroup = dtd.Opaque.TunnelGroup
	c.Prof.GroupAlias = dtd.Opaque.GroupAlias
	c.Prof.ConfigHash = dtd.Opaque.ConfigHash

	gps := len(dtd.Auth.Form.Groups)
	if gps != 0 && !utils.InArray(dtd.Auth.Form.Groups, c.Prof.Group) {
		return fmt.Errorf("available user groups are: %s", strings.Join(dtd.Auth.Form.Groups, " "))
	}

	c.Prof.Initialized = true
	return nil
}

// PasswordAuth выполняет аутентификацию. После успеха сервер создаёт
// ConnSession и возвращает SessionToken (или WebVpnCookie через заголовок —
// совместимость с OpenConnect/ocserv). Аналог auth.PasswordAuth.
func (c *Client) PasswordAuth() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	dtd := new(proto.DTD)
	// Отправляем имя пользователя или имя+пароль
	err := c.tplPost(tplAuthReply, c.Prof.AuthPath, dtd)
	if err != nil {
		return err
	}
	// Совместимость с двухшаговым логином: при необходимости шлём ещё раз
	if dtd.Type == "auth-request" && dtd.Auth.Error.Value == "" {
		dtd = new(proto.DTD)
		err = c.tplPost(tplAuthReply, c.Prof.AuthPath, dtd)
		if err != nil {
			return err
		}
	}
	// Ошибка имени пользователя, пароля и т.п.
	if dtd.Type == "auth-request" {
		if dtd.Auth.Error.Value != "" {
			return fmt.Errorf(dtd.Auth.Error.Value, dtd.Auth.Error.Param1)
		}
		return errors.New(dtd.Auth.Message)
	}

	// AnyConnect-клиенты получают токен в XML; ocserv/OpenConnect отдаёт
	// состояние логина через cookie webvpn
	c.SessionToken = dtd.SessionToken
	if c.WebVpnCookie != "" {
		c.SessionToken = c.WebVpnCookie
	}
	base.Debug("SessionToken:" + c.SessionToken)
	return nil
}

// Close закрывает TLS-соединение клиента.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.Conn == nil {
		return nil
	}
	err := c.Conn.Close()
	c.Conn = nil
	c.BufR = nil
	return err
}

// Cookie возвращает webvpn cookie (либо SessionToken, если сервер вернул
// токен в XML) — значение, которое используется при установлении CSTP-канала.
func (c *Client) Cookie() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.WebVpnCookie != "" {
		return c.WebVpnCookie
	}
	return c.SessionToken
}

// tplPost рендерит XML-шаблон и отправляет запрос по уже открытому
// TLS-соединению. Аналог auth.tplPost, но без глобалов: заголовки
// X-Transcend-Version / X-Aggregate-Auth ставятся напрямую (бывший reqHeaders).
func (c *Client) tplPost(typ int, path string, dtd *proto.DTD) error {
	tplBuffer := new(bytes.Buffer)
	if typ == tplInit {
		t, _ := template.New("init").Parse(templateInit)
		_ = t.Execute(tplBuffer, c.Prof)
	} else {
		t, _ := template.New("auth_reply").Parse(templateAuthReply)
		_ = t.Execute(tplBuffer, c.Prof)
	}
	if base.Cfg.LogLevel == "Debug" {
		post := tplBuffer.String()
		if typ == tplAuthReply {
			post = utils.RemoveBetween(post, "<auth>", "</auth>")
		}
		base.Debug(post)
	}
	url := fmt.Sprintf("%s%s%s", c.Prof.Scheme, c.Prof.HostWithPort, path)
	if c.Prof.SecretKey != "" {
		url += "?" + c.Prof.SecretKey
	}
	req, _ := http.NewRequest("POST", url, tplBuffer)

	utils.SetCommonHeader(req)
	req.Header["X-Transcend-Version"] = []string{"1"}
	req.Header["X-Aggregate-Auth"] = []string{"1"}

	err := req.Write(c.Conn)
	if err != nil {
		c.Conn.Close()
		return err
	}

	var resp *http.Response
	resp, err = http.ReadResponse(c.BufR, req)
	if err != nil {
		c.Conn.Close()
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.Conn.Close()
		return err
	}
	if base.Cfg.LogLevel == "Debug" {
		base.Debug(string(body))
	}

	if resp.StatusCode == http.StatusOK {
		err = xml.Unmarshal(body, dtd)
		if dtd.Type == "complete" && dtd.SessionToken == "" {
			// Совместимость с ocserv: токен приходит в cookie webvpn
			cookies := resp.Cookies()
			if len(cookies) != 0 {
				for _, ck := range cookies {
					if ck.Name == "webvpn" {
						c.WebVpnCookie = ck.Value
						break
					}
				}
			}
		}
		// nil при успешном разборе
		return err
	}
	c.Conn.Close()
	return fmt.Errorf("auth error %s", resp.Status)
}

// Events возвращает канал событий туннеля.
func (c *Client) Events() <-chan Event {
	if c.events == nil {
		c.events = make(chan Event, 16)
	}
	return c.events
}

// Submit2FA передаёт 2FA-код (TOTP), запрошенный сервером.
func (c *Client) Submit2FA(code string) {
	if c.twoFA == nil {
		c.twoFA = make(chan string, 1)
	}
	select {
	case c.twoFA <- code:
	default:
	}
}

// Needs2FA возвращает true если сервер запросил 2FA (по ошибке PasswordAuth).
func (c *Client) Needs2FA() bool {
	return false // TODO: детектировать по ответу сервера
}

// Connect выполняет полный цикл: auth → tunnel → packet flow.
// Не блокирует: статус приходит через Events().
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return fmt.Errorf("туннель %s уже запущен", c.Prof.Host)
	}
	c.running = true
	c.mu.Unlock()

	if c.events == nil {
		c.events = make(chan Event, 16)
	}
	if c.twoFA == nil {
		c.twoFA = make(chan string, 1)
	}

	go c.run(ctx)
	return nil
}

// Disconnect останавливает туннель.
func (c *Client) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running = false

	if c.tunnel != nil {
		_ = c.tunnel.Close()
	}
	_ = c.Close()
	return nil
}

// Connected возвращает true если туннель активен.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tunnel != nil && c.running
}

// Mode возвращает режим туннеля.
func (c *Client) Mode() string {
	// TODO: хранить режим в Client
	return "tun"
}

// run — главный цикл подключения.
func (c *Client) run(ctx context.Context) {
	defer func() {
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
		if c.events != nil {
			close(c.events)
		}
	}()

	c.emit(EventConnected, "Инициализация TLS...")
	if err := c.InitAuth(); err != nil {
		c.emit(EventError, fmt.Sprintf("InitAuth: %v", err))
		return
	}

	c.emit(EventConnected, "Аутентификация...")
	if err := c.PasswordAuth(); err != nil {
		c.emit(EventError, fmt.Sprintf("PasswordAuth: %v", err))
		return
	}

	c.emit(EventConnected, "Аутентификация успешна, установка туннеля...")

	tunnel, err := c.SetupTunnel("tun")
	if err != nil {
		c.emit(EventError, fmt.Sprintf("SetupTunnel: %v", err))
		return
	}
	c.tunnel = tunnel

	if err := tunnel.SetupTUN(""); err != nil {
		c.emit(EventError, fmt.Sprintf("SetupTUN: %v", err))
		return
	}
	c.emit(EventConnected, "TUN туннель установлен")

	select {
	case <-ctx.Done():
		c.emit(EventDisconnected, "Подключение отменено пользователем")
	case <-tunnel.Done():
		c.emit(EventDisconnected, "Туннель закрыт сервером")
	}
}

// emit кладёт событие в канал, не блокируясь.
func (c *Client) emit(t EventType, msg string) {
	if c.events == nil {
		return
	}
	select {
	case c.events <- Event{Type: t, Message: msg}:
	default:
	}
}

const templateInit = `<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="init" aggregate-auth-version="2">
    <version who="vpn">{{.AppVersion}}</version>
    <device-id computer-name="{{.ComputerName}}" device-type="{{.DeviceType}}" platform-version="{{.PlatformVersion}}" unique-id="{{.UniqueId}}"></device-id>
</config-auth>`

// https://datatracker.ietf.org/doc/html/draft-mavrogiannopoulos-openconnect-03#section-2.1.2.2
const templateAuthReply = `<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="auth-reply" aggregate-auth-version="2">
    <version who="vpn">{{.AppVersion}}</version>
    <device-id computer-name="{{.ComputerName}}" device-type="{{.DeviceType}}" platform-version="{{.PlatformVersion}}" unique-id="{{.UniqueId}}"></device-id>
    <opaque is-for="sg">
        <tunnel-group>{{.TunnelGroup}}</tunnel-group>
        <group-alias>{{.GroupAlias}}</group-alias>
        <config-hash>{{.ConfigHash}}</config-hash>
    </opaque>
    <mac-address-list>
        <mac-address public-interface="true">{{.MacAddress}}</mac-address>
    </mac-address-list>
    <auth>
        <username>{{.Username}}</username>
        <password>{{.Password}}</password>
    </auth>
    <group-select>{{.Group}}</group-select>
</config-auth>`
