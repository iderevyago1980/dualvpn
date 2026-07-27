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
	Mode               string // ModeTUN (по умолчанию) или ModeSOCKS5
	TunName            string // Имя TUN-интерфейса в режиме tun (напр. "dualvpn0")
}

// ErrNeeds2FA возвращается из PasswordAuth, когда сервер запросил второй
// фактор (форму с полем для кода). Код передаётся через Submit2FA.
var ErrNeeds2FA = errors.New("sslcon: сервер запросил 2FA-код")

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

	// Состояние 2FA: сервер прислал форму для второго фактора.
	needs2FA     bool
	twoFACode    string // код, подставляемый в шаблон при отправке (только под mu)
	lastAuthBody []byte // сырое тело последнего ответа аутентификации (для разбора формы 2FA)

	// TunnelSetup — необязательный хук: вызывается из run() вместо
	// SetupTUN после установки CSTP-туннеля. В режиме SOCKS5 менеджер
	// подставляет сюда создание Bridge поверх PacketFlow.
	TunnelSetup func(t *Tunnel) error

	// Поля для high-level API (совместимость с openconnect.Client).
	mode    string        // ModeTUN или ModeSOCKS5
	tunName string        // имя TUN-интерфейса (режим tun)
	events  chan Event
	twoFAOK chan struct{} // сигнал run(): Submit2FA успешно прошла
	tunnel  *Tunnel
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

// Типы XML-шаблонов (как в sslcon/auth, плюс ответ на 2FA-challenge).
const (
	tplInit = iota
	tplAuthReply
	tpl2FAReply
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
		// utils.SetCommonHeader при CiscoCompat=true переписывает
		// base.Cfg.AgentName на каждый запрос — при нескольких туннелях
		// это гонка записи в общий глобал (-race). Фиксируем значение
		// один раз: условие записи в SetCommonHeader становится ложным,
		// а User-Agent остаётся "AnyConnect ...".
		base.Cfg.AgentName = "AnyConnect"
		base.Cfg.CiscoCompat = false
	})
}

// NewClient создаёт клиент аутентификации для одного туннеля.
func NewClient(cfg ClientConfig) *Client {
	ensureBase()
	mode := cfg.Mode
	if mode == "" {
		mode = ModeTUN
	}
	return &Client{
		Prof:               NewProfile(cfg.Host, cfg.Username, cfg.Password, cfg.Group, cfg.SecretKey),
		insecureSkipVerify: cfg.InsecureSkipVerify,
		mode:               mode,
		tunName:            cfg.TunName,
		twoFAOK:            make(chan struct{}, 1),
		// Канал событий создаётся сразу: Manager.Start вызывает Events()
		// из отдельной горутины одновременно с Connect() — ленивая
		// инициализация в обоих местах давала гонку.
		events: make(chan Event, 16),
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
	// <group-select> отправляем только когда сервер реально предложил список
	// групп. ocserv без select-group групп не предлагает и отвергает
	// непрошеный group-select (реальный 401), поэтому там флаг остаётся false.
	c.Prof.SendGroupSelect = gps != 0

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
	// Сервер запросил второй фактор (challenge-форма с полем для кода)?
	// Проверяем до повторной отправки, иначе challenge будет «съеден»
	// повторным auth-reply с паролем вместо кода.
	if err := c.check2FAChallenge(dtd); err != nil {
		return err
	}
	// Совместимость с двухшаговым логином: при необходимости шлём ещё раз
	if dtd.Type == "auth-request" && dtd.Auth.Error.Value == "" {
		dtd = new(proto.DTD)
		err = c.tplPost(tplAuthReply, c.Prof.AuthPath, dtd)
		if err != nil {
			return err
		}
		if err := c.check2FAChallenge(dtd); err != nil {
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

// challengeForm — разбор challenge-ответа сервера: auth-request с формой,
// содержащей <input> для второго фактора. proto.DTD не разбирает <input>,
// поэтому сырое тело ответа парсится отдельно.
type challengeForm struct {
	XMLName xml.Name `xml:"config-auth"`
	Type    string   `xml:"type,attr"`
	Auth    struct {
		ID      string `xml:"id,attr"`
		Message string `xml:"message"`
		Form    struct {
			Action string `xml:"action,attr"`
			Inputs []struct {
				Type string `xml:"type,attr"`
				Name string `xml:"name,attr"`
			} `xml:"input"`
		} `xml:"form"`
	} `xml:"auth"`
}

// detect2FAForm определяет, является ли тело ответа challenge-формой 2FA.
// Признаки: type="auth-request" и (auth id="challenge" ИЛИ форма с <input>
// для кода — любое поле, кроме username/password первичного логина).
// Возвращает action формы (куда слать код) и сообщение сервера.
func detect2FAForm(body []byte) (is2FA bool, action, message string) {
	var cf challengeForm
	if err := xml.Unmarshal(body, &cf); err != nil {
		return false, "", ""
	}
	if cf.Type != "auth-request" {
		return false, "", ""
	}
	if cf.Auth.ID == "challenge" {
		return true, cf.Auth.Form.Action, cf.Auth.Message
	}
	for _, in := range cf.Auth.Form.Inputs {
		switch strings.ToLower(in.Name) {
		case "username", "password", "group_list", "":
			// поля первичного логина — не 2FA
		default:
			// secondary_password, answer, otp, code и т.п.
			return true, cf.Auth.Form.Action, cf.Auth.Message
		}
	}
	return false, "", ""
}

// check2FAChallenge проверяет последний ответ сервера на challenge-форму 2FA.
// При обнаружении сохраняет состояние (needs2FA, action формы) и возвращает
// ErrNeeds2FA. Вызывается под c.mu.
func (c *Client) check2FAChallenge(dtd *proto.DTD) error {
	if dtd.Type != "auth-request" || dtd.Auth.Error.Value != "" {
		return nil
	}
	is2FA, action, message := detect2FAForm(c.lastAuthBody)
	if !is2FA {
		return nil
	}
	if action != "" {
		c.Prof.AuthPath = action // код отправляется на action challenge-формы
	}
	c.needs2FA = true
	if message != "" {
		return fmt.Errorf("%w: %s", ErrNeeds2FA, message)
	}
	return ErrNeeds2FA
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
	switch typ {
	case tplInit:
		t, _ := template.New("init").Parse(templateInit)
		_ = t.Execute(tplBuffer, c.Prof)
	case tpl2FAReply:
		t, _ := template.New("2fa_reply").Parse(template2FAReply)
		_ = t.Execute(tplBuffer, struct {
			*Profile
			Code string
		}{c.Prof, c.twoFACode})
	default:
		t, _ := template.New("auth_reply").Parse(templateAuthReply)
		_ = t.Execute(tplBuffer, c.Prof)
	}
	if base.Cfg.LogLevel == "Debug" {
		post := tplBuffer.String()
		if typ != tplInit {
			// не логируем пароль и 2FA-код
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
	// Сырое тело нужно для разбора challenge-формы 2FA (proto.DTD не
	// разбирает <input> внутри <form>)
	c.lastAuthBody = body

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

// Events возвращает канал событий туннеля. Канал создаётся в NewClient,
// поэтому вызов безопасен из любой горутины.
func (c *Client) Events() <-chan Event {
	return c.events
}

// Submit2FA отправляет серверу 2FA-код в ответ на challenge-форму.
// Вызывается после того, как PasswordAuth вернул ErrNeeds2FA. При неверном
// коде возвращает ошибку — код можно запросить у пользователя повторно.
func (c *Client) Submit2FA(code string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.needs2FA {
		return errors.New("sslcon: сервер не запрашивал 2FA-код")
	}
	if c.Conn == nil || c.BufR == nil {
		return errors.New("sslcon: нет TLS-соединения для отправки 2FA-кода")
	}

	c.twoFACode = code
	dtd := new(proto.DTD)
	err := c.tplPost(tpl2FAReply, c.Prof.AuthPath, dtd)
	c.twoFACode = ""
	if err != nil {
		return err
	}

	if dtd.Type == "auth-request" {
		// Сервер прислал новую challenge-форму или ошибку — код не принят
		if is2FA, action, message := detect2FAForm(c.lastAuthBody); is2FA {
			if action != "" {
				c.Prof.AuthPath = action
			}
			if message != "" {
				return fmt.Errorf("sslcon: сервер отклонил 2FA-код: %s", message)
			}
			return errors.New("sslcon: сервер отклонил 2FA-код")
		}
		if dtd.Auth.Error.Value != "" {
			return fmt.Errorf(dtd.Auth.Error.Value, dtd.Auth.Error.Param1)
		}
		return errors.New(dtd.Auth.Message)
	}

	// Успех: сервер вернул type="complete" с токеном (или cookie webvpn)
	c.needs2FA = false
	c.SessionToken = dtd.SessionToken
	if c.WebVpnCookie != "" {
		c.SessionToken = c.WebVpnCookie
	}
	base.Debug("SessionToken (2FA):" + c.SessionToken)

	// Сигнал run(): аутентификация завершена, можно ставить туннель
	select {
	case c.twoFAOK <- struct{}{}:
	default:
	}
	return nil
}

// Needs2FA возвращает true, если сервер запросил второй фактор и код
// ещё не был успешно отправлен.
func (c *Client) Needs2FA() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.needs2FA
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

	if c.twoFAOK == nil {
		c.twoFAOK = make(chan struct{}, 1)
	}

	go c.run(ctx)
	return nil
}

// Disconnect останавливает туннель.
func (c *Client) Disconnect() error {
	// Снимаем нужное под мьютексом и сразу отпускаем: Close() берёт c.mu
	// сам, а sync.Mutex не реентрантный — держать его здесь нельзя.
	c.mu.Lock()
	c.running = false
	tunnel := c.tunnel
	c.mu.Unlock()

	if tunnel != nil {
		_ = tunnel.Close()
	}
	return c.Close()
}

// Connected возвращает true если туннель активен.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tunnel != nil && c.running
}

// Mode возвращает режим туннеля (ModeTUN или ModeSOCKS5).
func (c *Client) Mode() string {
	return c.mode
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
		if !errors.Is(err, ErrNeeds2FA) {
			c.emit(EventError, fmt.Sprintf("PasswordAuth: %v", err))
			return
		}
		// Сервер запросил второй фактор: сообщаем UI и ждём, пока
		// Submit2FA (вызванная менеджером/UI) не пройдёт успешно
		c.emit(Event2FARequired, err.Error())
		select {
		case <-c.twoFAOK:
		case <-ctx.Done():
			c.emit(EventDisconnected, "Подключение отменено во время ожидания 2FA-кода")
			return
		}
	}

	c.emit(EventConnected, "Аутентификация успешна, установка туннеля...")

	tunnel, err := c.SetupTunnel(c.mode)
	if err != nil {
		c.emit(EventError, fmt.Sprintf("SetupTunnel: %v", err))
		return
	}
	c.tunnel = tunnel

	// Хук вызывающего (SOCKS5-режим: менеджер поднимает Bridge поверх
	// PacketFlow); без хука — стандартный TUN-адаптер
	if c.TunnelSetup != nil {
		if err := c.TunnelSetup(tunnel); err != nil {
			c.emit(EventError, fmt.Sprintf("настройка туннеля: %v", err))
			_ = tunnel.Close()
			return
		}
		c.emit(EventConnected, "Туннель установлен ("+c.mode+")")
	} else {
		if c.mode == ModeSOCKS5 {
			c.emit(EventError, "режим socks5 требует обработчика TunnelSetup")
			_ = tunnel.Close()
			return
		}
		if err := tunnel.SetupTUN(c.tunName); err != nil {
			c.emit(EventError, fmt.Sprintf("SetupTUN: %v", err))
			return
		}
		c.emit(EventConnected, "TUN туннель установлен")
	}

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
{{if .SendGroupSelect}}    <group-select>{{.Group}}</group-select>
{{end}}</config-auth>`

// Ответ на challenge-форму 2FA: в <password> подставляется код второго
// фактора (TOTP/SMS/push-код), как это делает официальный AnyConnect.
const template2FAReply = `<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="auth-reply" aggregate-auth-version="2">
    <version who="vpn">{{.AppVersion}}</version>
    <device-id computer-name="{{.ComputerName}}" device-type="{{.DeviceType}}" platform-version="{{.PlatformVersion}}" unique-id="{{.UniqueId}}"></device-id>
    <opaque is-for="sg">
        <tunnel-group>{{.TunnelGroup}}</tunnel-group>
        <group-alias>{{.GroupAlias}}</group-alias>
        <config-hash>{{.ConfigHash}}</config-hash>
    </opaque>
    <auth>
        <username>{{.Username}}</username>
        <password>{{.Code}}</password>
    </auth>
{{if .SendGroupSelect}}    <group-select>{{.Group}}</group-select>
{{end}}</config-auth>`
