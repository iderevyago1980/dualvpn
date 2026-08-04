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
	"net/url"
	"regexp"
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
	needs2FA      bool
	twoFACode     string // код, подставляемый в шаблон при отправке (только под mu)
	twoFAField    string // имя поля кода из challenge-формы (обычно answer)
	twoFAOpaque   string // <opaque> из challenge-ответа, возвращается серверу дословно
	twoFASendUser bool   // слать ли <username> — только если форма его содержит
	lastAuthBody  []byte // сырое тело последнего ответа аутентификации (для разбора формы 2FA)

	// serverGroups — список алиасов групп, предложенный сервером на шаге init.
	serverGroups []string

	// TunnelSetup — необязательный хук: вызывается из run() вместо
	// SetupTUN после установки CSTP-туннеля. В режиме SOCKS5 менеджер
	// подставляет сюда создание Bridge поверх PacketFlow.
	TunnelSetup func(t *Tunnel) error

	// Поля для high-level API (совместимость с openconnect.Client).
	mode    string // ModeTUN или ModeSOCKS5
	tunName string // имя TUN-интерфейса (режим tun)
	events  chan Event
	twoFAOK chan struct{} // сигнал run(): Submit2FA успешно прошла
	tunnel  *Tunnel
	running bool

	// stopChan закрывается в Disconnect и будит run(), если тот ждёт 2FA-код.
	// Без него отказ от ввода кода оставлял горутину run() заблокированной
	// навсегда: канал событий не закрывался, менеджер считал туннель
	// запущенным, интерфейс вечно показывал «подключение».
	stopChan chan struct{}
	stopOnce sync.Once
}

// EventType — тип события жизненного цикла туннеля.
// Совместим с vpn.EventType из openconnect-обёртки.
type EventType string

const (
	EventConnected    EventType = "connected"
	EventDisconnected EventType = "disconnected"
	EventError        EventType = "error"
	Event2FARequired  EventType = "2fa_required"
	// EventProgress — промежуточный шаг подключения (TLS, аутентификация).
	// Раньше эти шаги слались как EventConnected, из-за чего Manager помечал
	// туннель подключённым сразу после начала TLS, а UI зажигал зелёный
	// индикатор до того, как аутентификация вообще состоялась.
	EventProgress EventType = "progress"
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
		stopChan:           make(chan struct{}),
		// Канал событий создаётся сразу: Manager.Start вызывает Events()
		// из отдельной горутины одновременно с Connect() — ленивая
		// инициализация в обоих местах давала гонку.
		events: make(chan Event, 16),
	}
}

// maxRedirects ограничивает цепочку перенаправлений: несколько шагов
// допустимы (имя → балансировщик → узел), бесконечный цикл — нет.
const maxRedirects = 5

// initWithRedirects выполняет init-запрос, следуя перенаправлениям шлюза.
// При каждом переходе адрес профиля обновляется, поэтому аутентификация и
// CSTP-туннель дальше идут уже на конечный сервер.
func (c *Client) initWithRedirects(dtd *proto.DTD) error {
	for attempt := 0; ; attempt++ {
		if err := c.dial(); err != nil {
			return err
		}

		err := c.tplPost(tplInit, "", dtd)
		if err == nil {
			return nil
		}

		var redirect *redirectError
		if !errors.As(err, &redirect) {
			return err
		}
		if attempt >= maxRedirects {
			return fmt.Errorf("слишком много перенаправлений (%d), последнее — на %s",
				maxRedirects, redirect.Location)
		}

		host, hostErr := redirectHost(redirect.Location)
		if hostErr != nil {
			return hostErr
		}
		if host == c.Prof.HostWithPort || host == c.Prof.Host {
			return fmt.Errorf("сервер перенаправляет сам на себя (%s)", redirect.Location)
		}

		base.Info("сервер перенаправил " + c.Prof.Host + " на " + host)
		c.emit(EventProgress, fmt.Sprintf("сервер перенаправил на %s", host))
		c.Prof.SetHost(host)
	}
}

// dial устанавливает TLS-соединение с текущим адресом профиля.
func (c *Client) dial() error {
	config := tls.Config{
		InsecureSkipVerify: c.insecureSkipVerify,
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 6 * time.Second}, "tcp4", c.Prof.HostWithPort, &config)
	if err != nil {
		return err
	}
	c.Conn = conn
	c.BufR = bufio.NewReader(conn)
	return nil
}

// InitAuth устанавливает TLS-соединение, определяет группу пользователя и
// адрес аутентификации AuthPath. Аналог auth.InitAuth, но на состоянии Client.
//
// Перенаправления шлюза отслеживаются: адрес, на который сервер отвечает
// редиректом, заменяется на целевой (см. initWithRedirects).
func (c *Client) InitAuth() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.WebVpnCookie = ""
	c.Prof.AppVersion = base.Cfg.AgentVersion
	c.Prof.MacAddress = base.LocalInterface.Mac

	dtd := new(proto.DTD)
	if err := c.initWithRedirects(dtd); err != nil {
		return err
	}
	c.Prof.AuthPath = dtd.Auth.Form.Action
	if c.Prof.AuthPath == "" {
		c.Prof.AuthPath = "/"
	}
	c.Prof.TunnelGroup = dtd.Opaque.TunnelGroup
	c.Prof.GroupAlias = dtd.Opaque.GroupAlias
	c.Prof.ConfigHash = dtd.Opaque.ConfigHash

	c.serverGroups = dtd.Auth.Form.Groups
	gps := len(dtd.Auth.Form.Groups)
	if gps != 0 && c.Prof.Group != "" && !utils.InArray(dtd.Auth.Form.Groups, c.Prof.Group) {
		// Сервер отдаёт список алиасов групп; имя из конфига должно совпасть
		// с одним из них буквально, иначе подключение бессмысленно продолжать.
		return fmt.Errorf("группа %q не найдена на сервере; доступные группы: %s",
			c.Prof.Group, strings.Join(dtd.Auth.Form.Groups, ", "))
	}
	// <group-select> отправляем только когда сервер реально предложил список
	// групп И группа выбрана. Пустая группа означает «использовать группу по
	// умолчанию»: сервер сам подставит ту, что помечена selected. ocserv без
	// select-group групп не предлагает и отвергает непрошеный group-select
	// (реальный 401), поэтому там флаг остаётся false.
	c.Prof.SendGroupSelect = gps != 0 && c.Prof.Group != ""

	c.Prof.Initialized = true
	return nil
}

// ServerGroups возвращает список групп, предложенных сервером на шаге init
// (пусто, пока InitAuth не выполнена или если сервер групп не предлагает).
func (c *Client) ServerGroups() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.serverGroups...)
}

// FetchGroups спрашивает у сервера список групп (алиасов tunnel-group).
// Учётные данные не нужны: список приходит в ответе на init, до логина.
// Нужен интерфейсу, чтобы имя группы выбиралось из списка, а не угадывалось:
// оно должно совпасть с алиасом буквально.
func FetchGroups(host string, insecureSkipVerify bool) ([]string, error) {
	if host == "" {
		return nil, errors.New("sslcon: не указан адрес сервера")
	}
	// Группа намеренно пустая: на этом шаге мы её и выясняем.
	c := NewClient(ClientConfig{Host: host, InsecureSkipVerify: insecureSkipVerify})
	defer c.Close() //nolint:errcheck // соединение открывалось только ради списка групп

	if err := c.InitAuth(); err != nil {
		return nil, err
	}
	return c.ServerGroups(), nil
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
			// Текст ошибки — шаблон с подстановкой param1, но чаще всего
			// без %s ("Authentication failed."). Безусловный Errorf дописывал
			// к нему мусор вида %!(EXTRA string=).
			return errors.New(formatServerMessage(dtd.Auth.Error.Value, dtd.Auth.Error.Param1, ""))
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
	// Блок <opaque> challenge-ответа отличается от того, что пришёл на init:
	// ASA кладёт в него <auth-handle>, которым связывает challenge с сессией.
	// Его нужно вернуть дословно, поэтому храним сырое содержимое.
	Opaque struct {
		Inner string `xml:",innerxml"`
	} `xml:"opaque"`
	Auth struct {
		ID string `xml:"id,attr"`
		// Cisco ASA отдаёт текст запроса как шаблон в <message> и подстановки
		// в его атрибутах: <message id="2" param1="Введите OTP-код">%s</message>.
		// Без подстановки пользователь видел в модалке буквальное "%s".
		Message struct {
			Value  string `xml:",chardata"`
			Param1 string `xml:"param1,attr"`
			Param2 string `xml:"param2,attr"`
		} `xml:"message"`
		Form struct {
			Action string `xml:"action,attr"`
			Inputs []struct {
				Type string `xml:"type,attr"`
				Name string `xml:"name,attr"`
			} `xml:"input"`
		} `xml:"form"`
	} `xml:"auth"`
}

// twoFAChallenge — разобранная challenge-форма второго фактора.
type twoFAChallenge struct {
	Action      string // куда слать код (action формы)
	Message     string // текст запроса для пользователя
	Field       string // имя элемента, в котором сервер ждёт код
	HasUsername bool   // форма содержит поле username — только тогда его слать
	Opaque      string // содержимое <opaque> challenge-ответа, вернуть дословно
}

// detect2FAForm определяет, является ли тело ответа challenge-формой 2FA.
// Признаки: type="auth-request" и (auth id="challenge" ИЛИ форма с <input>
// для кода — любое поле, кроме username/password первичного логина).
func detect2FAForm(body []byte) (*twoFAChallenge, bool) {
	var cf challengeForm
	if err := xml.Unmarshal(body, &cf); err != nil {
		return nil, false
	}
	if cf.Type != "auth-request" {
		return nil, false
	}

	field := cf.codeField()
	if cf.Auth.ID != "challenge" && field == "" {
		return nil, false
	}
	return &twoFAChallenge{
		Action:      cf.Auth.Form.Action,
		Message:     cf.challengeMessage(),
		Field:       field,
		HasUsername: cf.hasInput("username"),
		Opaque:      strings.TrimSpace(cf.Opaque.Inner),
	}, true
}

// hasInput сообщает, есть ли в форме поле с указанным именем.
func (cf *challengeForm) hasInput(name string) bool {
	for _, in := range cf.Auth.Form.Inputs {
		if strings.EqualFold(in.Name, name) {
			return true
		}
	}
	return false
}

// codeElement отображает имя поля формы на имя XML-элемента ответа.
// Cisco ASA называет поле challenge-формы answer (реже whichpin/new_password),
// но значение ждёт в элементе <password> — так же поступает OpenConnect
// (xmlpost_append_form_opts). Отправка <answer> приводит к «Login failed.».
func codeElement(field string) string {
	switch strings.ToLower(field) {
	case "", "answer", "whichpin", "new_password":
		return "password"
	default:
		// secondary_password и прочие имена уходят как есть.
		return field
	}
}

// codeField возвращает имя поля формы, в котором сервер ждёт код второго
// фактора. Клиент обязан назвать элемент в <auth> именно так, как поле
// названо в форме: реальная ASA просит <answer>, мок — <secondary_password>.
// Раньше код всегда уходил в <password>, и живой сервер отвечал
// «Login failed.» на верный код.
func (cf *challengeForm) codeField() string {
	for _, in := range cf.Auth.Form.Inputs {
		if strings.EqualFold(in.Type, "submit") || strings.EqualFold(in.Type, "hidden") {
			continue
		}
		switch strings.ToLower(in.Name) {
		case "username", "password", "group_list", "":
			// поля первичного логина — не второй фактор
		default:
			return in.Name
		}
	}
	return ""
}

// challengeMessage собирает читаемый текст запроса второго фактора.
func (cf *challengeForm) challengeMessage() string {
	return formatServerMessage(cf.Auth.Message.Value, cf.Auth.Message.Param1, cf.Auth.Message.Param2)
}

// applyChallenge запоминает параметры challenge-формы, нужные для ответа:
// адрес отправки, имя поля с кодом и блок <opaque>. Вызывается под c.mu.
func (c *Client) applyChallenge(ch *twoFAChallenge) {
	if ch.Action != "" {
		c.Prof.AuthPath = ch.Action // код отправляется на action challenge-формы
	}
	c.twoFAField = ch.Field
	c.twoFAOpaque = ch.Opaque
	c.twoFASendUser = ch.HasUsername
}

// formatServerMessage приводит сообщение Cisco ASA к читаемому виду.
// ASA отдаёт текст как шаблон, а подстановки — отдельными атрибутами:
//
//	<message id="2" param1="Введите OTP-код" param2="">%s</message>
//	<error id="16" param1="" param2="">Login error.</error>
//
// Шаблон может не содержать ни одного %s (тогда подстановки не нужны) или
// быть пустым (тогда весь текст — в параметрах). Безусловный Sprintf в обоих
// случаях портил сообщение: пользователь видел литеральное "%s" в модалке
// 2FA и "Authentication failed.%!(EXTRA string=)" в журнале.
func formatServerMessage(tpl, param1, param2 string) string {
	tpl = strings.TrimSpace(tpl)
	p1 := strings.TrimSpace(param1)
	p2 := strings.TrimSpace(param2)

	if tpl == "" {
		return strings.TrimSpace(p1 + " " + p2)
	}
	n := strings.Count(tpl, "%s")
	if n == 0 {
		return tpl
	}
	// Подставляем ровно по количеству %s: лишние аргументы Sprintf дописал бы
	// как %!(EXTRA...), недостающие — как %!s(MISSING).
	args := make([]any, 0, n)
	for i := 0; i < n; i++ {
		switch i {
		case 0:
			args = append(args, p1)
		case 1:
			args = append(args, p2)
		default:
			args = append(args, "")
		}
	}
	return strings.TrimSpace(fmt.Sprintf(tpl, args...))
}

// check2FAChallenge проверяет последний ответ сервера на challenge-форму 2FA.
// При обнаружении сохраняет состояние (needs2FA, action формы) и возвращает
// ErrNeeds2FA. Вызывается под c.mu.
func (c *Client) check2FAChallenge(dtd *proto.DTD) error {
	if dtd.Type != "auth-request" || dtd.Auth.Error.Value != "" {
		return nil
	}
	ch, is2FA := detect2FAForm(c.lastAuthBody)
	if !is2FA {
		return nil
	}
	c.applyChallenge(ch)
	c.needs2FA = true
	if message := ch.Message; message != "" {
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

	// Соединение могло закрыться раньше — например, туннель завершил сам
	// сервер. Отключение обязано быть идемпотентным: иначе пользователь,
	// нажавший «Отключить» у уже упавшего туннеля, получает ошибку
	// «use of closed network connection» вместо спокойного отключения.
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
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

// secretElement — элементы блока <auth>, значения которых нельзя писать в лог.
var secretElement = regexp.MustCompile(`(?s)<(password|answer|secondary_password|otp|code|passwd)>.*?</(?:password|answer|secondary_password|otp|code|passwd)>`)

// maskSecrets прячет значения паролей и кодов, оставляя имена элементов.
// Раньше вырезался весь блок <auth>...</auth> целиком — и по логу нельзя
// было понять, в каком элементе ушёл код второго фактора, а именно там и
// была причина отказов реальной ASA.
func maskSecrets(body string) string {
	return secretElement.ReplaceAllString(body, "<$1>***</$1>")
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
			Code         string
			CodeField    string
			OpaqueXML    string
			HasOpaque    bool
			SendUsername bool
		}{c.Prof, c.twoFACode, codeElement(c.twoFAField),
			c.twoFAOpaque, c.twoFAOpaque != "", c.twoFASendUser})
	default:
		t, _ := template.New("auth_reply").Parse(templateAuthReply)
		_ = t.Execute(tplBuffer, c.Prof)
	}
	if base.Cfg.LogLevel == "Debug" {
		post := tplBuffer.String()
		if typ != tplInit {
			post = maskSecrets(post)
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
	// Перенаправление: шлюз может отвечать с одного имени, а обслуживать
	// подключения на другом (например, vpn.example.com → vpn2.example.com).
	// Настоящий AnyConnect редирект отслеживает; без этого пользователь
	// получал невнятное «auth error 302 Temporary moved» и не понимал,
	// какой адрес указывать.
	if loc := redirectTarget(resp); loc != "" {
		c.Conn.Close()
		return &redirectError{Location: loc, Status: resp.Status}
	}

	c.Conn.Close()
	return fmt.Errorf("auth error %s", resp.Status)
}

// redirectError — сервер ответил перенаправлением на другой адрес.
type redirectError struct {
	Location string // значение заголовка Location
	Status   string // исходный статус ответа (для сообщения об ошибке)
}

func (e *redirectError) Error() string {
	return fmt.Sprintf("перенаправление (%s) на %s", e.Status, e.Location)
}

// redirectTarget возвращает адрес перенаправления ("" — ответ не является
// перенаправлением либо в нём нет Location).
func redirectTarget(resp *http.Response) string {
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return ""
	}
	return resp.Header.Get("Location")
}

// redirectHost разбирает Location и возвращает хост (с портом), на который
// нужно переключиться. Ошибка означает, что следовать перенаправлению
// нельзя, и её текст объясняет пользователю причину.
//
// Схема ограничена https: понижение до http означало бы отправку учётных
// данных открытым текстом, а на такое перенаправление идти нельзя.
func redirectHost(location string) (string, error) {
	u, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("некорректный адрес перенаправления %q: %w", location, err)
	}
	if u.Host == "" {
		// Перенаправление на другой путь того же сервера: адрес шлюза
		// остаётся прежним, менять нечего, а путь aggregate-auth задан
		// протоколом. Обычно это признак портала вместо VPN-эндпоинта.
		return "", fmt.Errorf("сервер перенаправляет на %q — похоже, это веб-портал, а не адрес VPN-шлюза", location)
	}
	if u.Scheme != "" && u.Scheme != "https" {
		return "", fmt.Errorf("перенаправление на %q: поддерживается только https", location)
	}
	return u.Host, nil
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
		if ch, is2FA := detect2FAForm(c.lastAuthBody); is2FA {
			// Сервер выдал новую challenge-форму: у неё свой auth-handle,
			// поэтому состояние обновляем перед повторной попыткой.
			c.applyChallenge(ch)
			msg := "сервер отклонил 2FA-код"
			if ch.Message != "" {
				msg += ": " + ch.Message
			}
			// Просим код заново: run() всё ещё ждёт, и без нового запроса
			// интерфейс закрыл бы окно ввода и завис бы в «подключении».
			c.emit(Event2FARequired, msg)
			return fmt.Errorf("sslcon: %s", msg)
		}
		// Повторная форма не пришла — попытка исчерпана, дальше ждать нечего.
		c.failAuth()
		if dtd.Auth.Error.Value != "" {
			return errors.New(formatServerMessage(dtd.Auth.Error.Value, dtd.Auth.Error.Param1, ""))
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

// failAuth прекращает ожидание кода в run(): сервер отверг аутентификацию
// окончательно и новой challenge-формы не прислал. Вызывается под c.mu.
// Без этого run() ждал бы код, которого уже некому принять.
func (c *Client) failAuth() {
	c.needs2FA = false
	c.stopOnce.Do(func() { close(c.stopChan) })
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

// Disconnect останавливает туннель. Если подключение ещё ждёт 2FA-код,
// ожидание прерывается — иначе горутина run() осталась бы висеть.
func (c *Client) Disconnect() error {
	c.stopOnce.Do(func() { close(c.stopChan) })

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

	c.emit(EventProgress, "Инициализация TLS...")
	if err := c.InitAuth(); err != nil {
		c.emit(EventError, fmt.Sprintf("InitAuth: %v", err))
		return
	}

	c.emit(EventProgress, "Аутентификация...")
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
		case <-c.stopChan:
			c.emit(EventDisconnected, "Подключение отменено во время ожидания 2FA-кода")
			return
		case <-ctx.Done():
			c.emit(EventDisconnected, "Подключение отменено во время ожидания 2FA-кода")
			return
		}
	}

	c.emit(EventProgress, "Аутентификация успешна, установка туннеля...")

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
    <device-id computer-name="{{.ComputerName}}" device-type="{{.DeviceType}}" platform-version="{{.PlatformVersion}}" unique-id="{{.UniqueId}}">{{.Platform}}</device-id>
</config-auth>`

// https://datatracker.ietf.org/doc/html/draft-mavrogiannopoulos-openconnect-03#section-2.1.2.2
const templateAuthReply = `<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="auth-reply" aggregate-auth-version="2">
    <version who="vpn">{{.AppVersion}}</version>
    <device-id computer-name="{{.ComputerName}}" device-type="{{.DeviceType}}" platform-version="{{.PlatformVersion}}" unique-id="{{.UniqueId}}">{{.Platform}}</device-id>
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

// Ответ на challenge-форму 2FA.
//
// Правило простое: в ответе повторяются ровно поля challenge-формы — так же
// строит запрос OpenConnect (xmlpost_append_form_opts). Отсюда три отличия
// от первичного auth-reply, каждое из которых по отдельности приводило к
// «Login failed.» на верный код:
//
//   - нет <username>: challenge-форма его не содержит (шлём только если
//     сервер всё же попросил — {{if .SendUsername}});
//   - нет <group-select>: группа выбрана на первом шаге, повторный выбор ASA
//     трактует как новую попытку первичного логина;
//   - <opaque> возвращается тот, что пришёл в challenge-ответе: в нём лежит
//     <auth-handle>, которым сервер связывает ответ с выданным challenge.
//
// Имя элемента с кодом даёт codeElement: поле формы answer соответствует
// элементу <password>.
const template2FAReply = `<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="auth-reply" aggregate-auth-version="2">
    <version who="vpn">{{.AppVersion}}</version>
    <device-id computer-name="{{.ComputerName}}" device-type="{{.DeviceType}}" platform-version="{{.PlatformVersion}}" unique-id="{{.UniqueId}}">{{.Platform}}</device-id>
    <opaque is-for="sg">{{if .HasOpaque}}{{.OpaqueXML}}{{else}}
        <tunnel-group>{{.TunnelGroup}}</tunnel-group>
        <group-alias>{{.GroupAlias}}</group-alias>
        <config-hash>{{.ConfigHash}}</config-hash>
    {{end}}</opaque>
    <auth>
{{if .SendUsername}}        <username>{{.Username}}</username>
{{end}}        <{{.CodeField}}>{{.Code}}</{{.CodeField}}>
    </auth>
</config-auth>`
