// Package sslcon — форк аутентификации sslcon (github.com/tlslink/sslcon)
// с состоянием на каждый туннель вместо package-level глобалов.
// Пакеты sslcon/base, sslcon/proto, sslcon/utils не форкаются — импортируются как есть.
package sslcon

import (
	"errors"
	"runtime"
	"strings"

	"github.com/elastic/go-sysinfo"
)

// Profile — параметры подключения и данные, полученные от шлюза в ходе
// аутентификации. Форк auth.Profile из sslcon: вместо глобала auth.Prof
// каждый туннель владеет своим экземпляром.
// Поля экспортируются, потому что подставляются в XML-шаблоны text/template.
type Profile struct {
	Host      string `json:"host"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	Group     string `json:"group"`
	SecretKey string `json:"secret"`

	Initialized bool
	AppVersion  string // версия клиента, сообщаемая серверу в XML

	HostWithPort string
	Scheme       string
	AuthPath     string

	MacAddress  string
	TunnelGroup string
	GroupAlias  string
	ConfigHash  string

	// SendGroupSelect — слать ли <group-select> в auth-reply. Ставится в
	// InitAuth: true, только если сервер предложил список групп. ocserv без
	// select-group групп не предлагает и отвергает непрошеный group-select
	// (реальный 401), поэтому в этом случае поле остаётся false.
	SendGroupSelect bool

	ComputerName    string
	DeviceType      string
	PlatformVersion string
	UniqueId        string
}

// NewProfile создаёт профиль подключения. Сведения об устройстве
// (hostname, тип ОС, unique-id) заполняются автоматически — аналог
// auth.init() из sslcon, но без глобального состояния.
func NewProfile(host, username, password, group, secret string) *Profile {
	p := &Profile{
		Host:      host,
		Username:  username,
		Password:  password,
		Group:     group,
		SecretKey: secret,
		Scheme:    "https://",
	}
	p.HostWithPort = hostWithPort(host)
	p.fillDeviceInfo()
	return p
}

// hostWithPort добавляет стандартный порт 443, если порт не указан явно.
func hostWithPort(host string) string {
	if strings.Contains(host, ":") {
		return host
	}
	return host + ":443"
}

// fillDeviceInfo заполняет атрибуты device-id для XML-запросов.
func (p *Profile) fillDeviceInfo() {
	host, err := sysinfo.Host()
	if err != nil {
		return
	}
	info := host.Info()
	p.ComputerName = info.Hostname
	p.UniqueId = info.UniqueID

	osInfo := info.OS
	p.DeviceType = osInfo.Name
	if runtime.GOOS == "windows" {
		p.PlatformVersion = osInfo.Build
	} else {
		p.PlatformVersion = strings.Split(osInfo.Version, " ")[0]
	}
}

// Validate проверяет обязательные поля профиля.
func (p *Profile) Validate() error {
	if p.Host == "" {
		return errors.New("sslcon: profile: не указан host")
	}
	if p.Username == "" {
		return errors.New("sslcon: profile: не указан username")
	}
	if p.Password == "" {
		return errors.New("sslcon: profile: не указан password")
	}
	return nil
}
