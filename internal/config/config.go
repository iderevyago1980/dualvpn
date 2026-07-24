// Package config отвечает за загрузку и сохранение конфигурации DualVPN
// в формате TOML (см. SPEC.md, раздел «Конфигурация»).
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Mode — глобальный режим работы приложения.
type Mode struct {
	// Preferred: "auto" | "tun" | "socks5".
	// auto — детекция админ-прав: admin → TUN, иначе SOCKS5.
	Preferred string `toml:"preferred"`
}

// Tunnel — параметры одного VPN-туннеля (Cisco AnyConnect эндпоинт).
type Tunnel struct {
	Name      string   `toml:"name"`       // Отображаемое имя туннеля (например, "VPN-1")
	Endpoint  string   `toml:"endpoint"`   // Адрес VPN-сервера (например, "vpn1.example.com")
	Group     string   `toml:"group"`      // Tunnel-group на Cisco ASA (например, "Group-2FA")
	SocksPort int      `toml:"socks_port"` // Локальный порт SOCKS5-прокси в режиме socks5
	TunName   string   `toml:"tun_name"`   // Имя TUN-интерфейса в режиме tun
	Routes    []string `toml:"routes"`     // Подсети для split-tunneling (CIDR)
	Username  string   `toml:"username"`   // Логин VPN (может быть пустым — запросим интерактивно)
	Password  string   `toml:"password"`   // Пароль VPN (может быть пустым — запросим интерактивно)
}

// Config — корневая структура конфигурационного файла.
type Config struct {
	Mode    Mode     `toml:"mode"`
	Tunnels []Tunnel `toml:"tunnels"`
}

// DefaultPath возвращает путь к пользовательскому конфигу в стандартном
// каталоге настроек ОС: $XDG_CONFIG_HOME/dualvpn/config.toml на Linux
// (обычно ~/.config/dualvpn/config.toml), %AppData%\dualvpn\config.toml
// на Windows. Нужен для запуска из меню/ярлыка, где рабочий каталог —
// корень или домашний, и относительный "config.toml" писать некуда.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("каталог конфигурации пользователя: %w", err)
	}
	return filepath.Join(dir, "dualvpn", "config.toml"), nil
}

// Default возвращает конфигурацию по умолчанию с двумя эндпоинтами из SPEC.md.
func Default() *Config {
	return &Config{
		Mode: Mode{Preferred: "auto"},
		Tunnels: []Tunnel{
			{
				Name:      "VPN-1",
				Endpoint:  "vpn1.example.com",
				Group:     "Group-2FA",
				SocksPort: 1080,
				TunName:   "vpn1",
				Routes:    []string{"192.168.10.0/24", "10.10.0.0/16"},
			},
			{
				Name:      "VPN-2",
				Endpoint:  "vpn2.example.com",
				Group:     "RA",
				SocksPort: 1081,
				TunName:   "vpn2",
				Routes:    []string{"192.168.20.0/24", "10.20.0.0/16"},
			},
		},
	}
}

// Load читает конфигурацию из TOML-файла по указанному пути.
func Load(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("чтение конфига %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("некорректный конфиг %s: %w", path, err)
	}
	return &cfg, nil
}

// Save сохраняет конфигурацию в TOML-файл, создавая каталог при необходимости.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("создание каталога конфига: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("запись конфига %s: %w", path, err)
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		return fmt.Errorf("сериализация конфига: %w", err)
	}
	return nil
}

// Validate проверяет базовую корректность конфигурации.
// Значение mode.preferred = "auto" валидно всегда: итоговый режим
// определяется автодетекцией админ-прав (см. internal/mode).
func (c *Config) Validate() error {
	switch c.Mode.Preferred {
	case "auto", "tun", "socks5":
	default:
		return fmt.Errorf("mode.preferred должен быть auto|tun|socks5, получено %q", c.Mode.Preferred)
	}
	seenPorts := map[int]string{}
	for i, t := range c.Tunnels {
		if t.Name == "" {
			return fmt.Errorf("tunnels[%d]: пустое имя", i)
		}
		if t.Endpoint == "" {
			return fmt.Errorf("туннель %q: пустой endpoint", t.Name)
		}
		if t.SocksPort <= 0 || t.SocksPort > 65535 {
			return fmt.Errorf("туннель %q: некорректный socks_port %d", t.Name, t.SocksPort)
		}
		if other, dup := seenPorts[t.SocksPort]; dup {
			return fmt.Errorf("туннели %q и %q используют один socks_port %d", other, t.Name, t.SocksPort)
		}
		seenPorts[t.SocksPort] = t.Name
	}
	return nil
}
