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
	Name      string   `toml:"name"`       // Отображаемое имя туннеля (например, "Офис")
	Endpoint  string   `toml:"endpoint"`   // Адрес VPN-сервера (например, "vpn.example.com")
	Group     string   `toml:"group"`      // Tunnel-group на Cisco ASA (например, "Group-2FA")
	SocksPort int      `toml:"socks_port"` // Локальный порт SOCKS5-прокси в режиме socks5
	TunName   string   `toml:"tun_name"`   // Имя TUN-интерфейса в режиме tun
	Routes    []string `toml:"routes"`     // Подсети для split-tunneling (CIDR)
	Username  string   `toml:"username"`   // Логин VPN (может быть пустым — запросим интерактивно)
	Password  string   `toml:"password"`   // Пароль VPN (может быть пустым — запросим интерактивно)
	ProbeURL  string   `toml:"probe_url"`  // URL внутри VPN для проверки связности на стенде (E2E)
}

// PAC — автонастройка прокси для браузера (режим socks5).
type PAC struct {
	// Port — локальный порт раздачи proxy.pac. 0 — порт по умолчанию
	// (DefaultPACPort); фиксированный порт важен, потому что адрес
	// прописывается в настройках браузера один раз.
	Port int `toml:"port"`
}

// DefaultPACPort — порт раздачи PAC-файла, если он не задан в конфиге.
const DefaultPACPort = 1088

// Config — корневая структура конфигурационного файла.
type Config struct {
	Mode    Mode     `toml:"mode"`
	PAC     PAC      `toml:"pac"`
	Tunnels []Tunnel `toml:"tunnels"`
}

// PACPort возвращает порт раздачи PAC с учётом значения по умолчанию.
func (c *Config) PACPort() int {
	if c.PAC.Port <= 0 || c.PAC.Port > 65535 {
		return DefaultPACPort
	}
	return c.PAC.Port
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

// Default возвращает пустую конфигурацию: только режим, без туннелей.
//
// Адреса серверов, группы, порты и маршруты — данные, а не код: они живут
// в config.example.toml, который встраивается в бинарь и разворачивается
// при первом запуске (см. CreateFrom). Раньше здесь лежал захардкоженный
// список эндпоинтов, и он успел разойтись с реальностью — имена групп в нём
// не совпадали ни с одним алиасом на живых серверах.
func Default() *Config {
	return &Config{Mode: Mode{Preferred: "auto"}}
}

// FromTOML разбирает конфигурацию из сырого TOML.
func FromTOML(data []byte) (*Config, error) {
	var cfg Config
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return nil, fmt.Errorf("разбор конфигурации: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("некорректная конфигурация: %w", err)
	}
	return &cfg, nil
}

// CreateFrom создаёт файл конфигурации по пути path из шаблона template,
// записывая его байт-в-байт: так пользователь получает файл с комментариями
// (какие бывают группы, что значит режим), а не голый вывод сериализатора.
// Пустой шаблон означает конфигурацию по умолчанию.
func CreateFrom(path string, template []byte) (*Config, error) {
	if len(template) == 0 {
		cfg := Default()
		if err := cfg.Save(path); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	cfg, err := FromTOML(template)
	if err != nil {
		return nil, fmt.Errorf("встроенный шаблон конфигурации: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("создание каталога конфига: %w", err)
	}
	// 0600: файл хранит логины и пароли.
	if err := os.WriteFile(path, template, 0o600); err != nil {
		return nil, fmt.Errorf("запись конфига %s: %w", path, err)
	}
	return cfg, nil
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
	seenNames := map[string]struct{}{}
	for i, t := range c.Tunnels {
		if t.Name == "" {
			return fmt.Errorf("tunnels[%d]: пустое имя", i)
		}
		// Имя туннеля — его идентификатор: по нему менеджер хранит туннели
		// в карте, а интерфейс — статусы. Одноимённые туннели затирали бы
		// друг друга, и один из них молча исчезал бы из работы.
		if _, dup := seenNames[t.Name]; dup {
			return fmt.Errorf("несколько туннелей с именем %q — имена должны быть разными", t.Name)
		}
		seenNames[t.Name] = struct{}{}
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
