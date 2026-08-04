// Package ipc — протокол общения графического приложения со службой DualVPN.
//
// Служба работает под LocalSystem и держит TUN-туннели: создание адаптера,
// маршруты, DNS и правила NRPT требуют прав администратора, а обмен пакетами
// привязан к процессу, создавшему адаптер. Приложение запускается обычным
// пользователем и управляет службой через именованный канал — благодаря
// этому админ-права нужны один раз, при установке.
//
// Формат: JSON по строке на кадр в обе стороны. От клиента идут запросы, от
// службы — ответы и события туннелей (последние приходят без запроса).
//
// # Безопасность
//
// Канал доступен обычным пользователям, то есть непривилегированный процесс
// передаёт данные привилегированному. Всё, что попадает в командные строки
// netsh/route/PowerShell или в имена интерфейсов, обязано проверяться до
// исполнения — см. Validate у параметров запросов.
package ipc

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PipeName — адрес именованного канала службы.
const PipeName = `\\.\pipe\dualvpn`

// Методы протокола.
const (
	MethodStatus        = "status"        // состояние всех туннелей
	MethodConnect       = "connect"       // поднять туннель
	MethodDisconnect    = "disconnect"    // остановить туннель
	MethodDisconnectAll = "disconnectAll" // остановить все туннели
	MethodSubmit2FA     = "submit2fa"     // передать код второго фактора
	MethodVersion       = "version"       // версия службы (проверка совместимости)
)

// Request — запрос от приложения к службе.
type Request struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response — ответ службы на запрос с тем же ID.
type Response struct {
	ID     uint64          `json:"id"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// Event — событие туннеля, которое служба рассылает подключённым клиентам
// без запроса (подключение, отключение, запрос 2FA, ошибка).
type Event struct {
	TunnelID string `json:"tunnelId"`
	Type     string `json:"type"`
	Message  string `json:"message"`
}

// Frame — один кадр канала: либо ответ на запрос, либо событие.
// Разделение по полям избавляет от угадывания типа при разборе.
type Frame struct {
	Response *Response `json:"response,omitempty"`
	Event    *Event    `json:"event,omitempty"`
}

// ConnectParams — параметры подключения одного туннеля. Учётные данные
// передаются на время сеанса и в службе не сохраняются.
type ConnectParams struct {
	ID       string   `json:"id"`
	Host     string   `json:"host"`
	Group    string   `json:"group"`
	Username string   `json:"username"`
	Password string   `json:"password"`
	TunName  string   `json:"tunName"`
	Routes   []string `json:"routes"`
}

// TunnelState — состояние туннеля в ответе на MethodStatus.
type TunnelState struct {
	ID        string `json:"id"`
	Connected bool   `json:"connected"`
}

// IDParams — параметры запросов, адресованных одному туннелю.
type IDParams struct {
	ID string `json:"id"`
}

// TwoFAParams — код второго фактора для туннеля.
type TwoFAParams struct {
	ID   string `json:"id"`
	Code string `json:"code"`
}

// dangerous — символы, которыми можно разорвать XML-запрос к шлюзу или
// команду оболочки. В именах туннелей и групп они не встречаются.
const dangerous = "\"'&|<>`$\\;\x00"

// safeText проверяет строку, приходящую от непривилегированного процесса:
// разрешены буквы любого алфавита (имена туннелей и групп бывают русскими),
// цифры, пробелы и обычная пунктуация — но не управляющие символы и не то,
// чем можно вклиниться в команду или XML.
func safeText(s string, maxRunes int) bool {
	if s == "" || utf8.RuneCountInString(s) > maxRunes {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || strings.ContainsRune(dangerous, r) {
			return false
		}
	}
	return true
}

// safeHost — адрес шлюза: имя или адрес, при желании с портом. Пробелы и
// управляющие символы исключены, чтобы строка не могла разорвать команду
// или URL.
var safeHost = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,253}(:[0-9]{1,5})?$`)

// Validate проверяет параметры подключения перед исполнением их службой.
func (p ConnectParams) Validate() error {
	if !safeText(p.ID, 64) {
		return fmt.Errorf("недопустимое имя туннеля %q", p.ID)
	}
	if !safeHost.MatchString(p.Host) {
		return fmt.Errorf("недопустимый адрес сервера %q", p.Host)
	}
	// Имя интерфейса необязательно: пустое означает сгенерированное службой.
	// Оно уходит в командные строки netsh и route, поэтому проверка строгая.
	if p.TunName != "" && !safeInterfaceName(p.TunName) {
		return fmt.Errorf("недопустимое имя интерфейса %q", p.TunName)
	}
	// Группа попадает в XML-запрос к шлюзу; пустая означает группу сервера
	// по умолчанию.
	if p.Group != "" && !safeText(p.Group, 128) {
		return fmt.Errorf("недопустимое имя группы %q", p.Group)
	}
	for _, r := range p.Routes {
		if strings.ContainsAny(r, " \t\r\n\"'&|<>") {
			return fmt.Errorf("недопустимый маршрут %q", r)
		}
	}
	// Учётные данные в командные строки не попадают, поэтому ограничиваем
	// только длину и управляющие символы: пароль вправе содержать почти
	// любые знаки.
	if utf8.RuneCountInString(p.Username) > 128 || utf8.RuneCountInString(p.Password) > 256 {
		return errors.New("слишком длинные учётные данные")
	}
	if strings.ContainsAny(p.Username, "\r\n\x00") || strings.ContainsAny(p.Password, "\r\n\x00") {
		return errors.New("недопустимые символы в учётных данных")
	}
	return nil
}

// safeInterfaceName — имя сетевого интерфейса без пробелов: оно уходит в
// netsh как отдельный аргумент и в имя адаптера Wintun.
func safeInterfaceName(name string) bool {
	if len(name) == 0 || len(name) > 32 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// Validate проверяет идентификатор туннеля.
func (p IDParams) Validate() error {
	if !safeText(p.ID, 64) {
		return fmt.Errorf("недопустимое имя туннеля %q", p.ID)
	}
	return nil
}

// Validate проверяет код второго фактора: только цифры и буквы, разумной
// длины — код уходит в XML-запрос к шлюзу.
func (p TwoFAParams) Validate() error {
	if !safeText(p.ID, 64) {
		return fmt.Errorf("недопустимое имя туннеля %q", p.ID)
	}
	if p.Code == "" || len(p.Code) > 32 || strings.ContainsAny(p.Code, "<>&\"'\r\n") {
		return errors.New("недопустимый код второго фактора")
	}
	return nil
}
