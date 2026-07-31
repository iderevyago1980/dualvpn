package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Шаблон конфигурации для тестов: эндпоинты в коде больше не живут
// (стартовый конфиг разворачивается из встроенного config.example.toml).
const testTemplate = `[mode]
preferred = "auto"

[[tunnels]]
name = "Первый"
endpoint = "vpn1.example.test"
group = "Группа A"
socks_port = 1080
tun_name = "tun-a"
routes = ["192.168.10.0/24"]

[[tunnels]]
name = "Второй"
endpoint = "vpn2.example.test"
group = "Группа B"
socks_port = 1081
tun_name = "tun-b"
routes = ["192.168.20.0/24", "10.20.0.0/16"]
`

func TestDefaultValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("конфиг по умолчанию должен быть валиден: %v", err)
	}
	if len(cfg.Tunnels) != 0 {
		t.Errorf("в конфиге по умолчанию не должно быть захардкоженных туннелей, получено %d", len(cfg.Tunnels))
	}
}

// Боевой шаблон, встраиваемый в бинарь, обязан разбираться и проходить
// валидацию — иначе первый запуск на чистой машине падает.
func TestExampleConfigValid(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatalf("чтение config.example.toml: %v", err)
	}
	cfg, err := FromTOML(data)
	if err != nil {
		t.Fatalf("config.example.toml невалиден: %v", err)
	}
	if len(cfg.Tunnels) == 0 {
		t.Error("в примере конфигурации нет ни одного туннеля")
	}
}

func TestCreateFromKeepsTemplateBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	tpl := "# комментарий\n" + testTemplate
	cfg, err := CreateFrom(path, []byte(tpl))
	if err != nil {
		t.Fatalf("CreateFrom: %v", err)
	}
	if len(cfg.Tunnels) != 2 {
		t.Errorf("ожидалось 2 туннеля из шаблона, получено %d", len(cfg.Tunnels))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != tpl {
		t.Error("шаблон записан не байт-в-байт (потеряны комментарии)")
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.toml") // каталог создаётся Save
	orig, err := FromTOML([]byte(testTemplate))
	if err != nil {
		t.Fatalf("FromTOML: %v", err)
	}
	orig.Mode.Preferred = "socks5"
	orig.Tunnels[0].Username = "user1"

	if err := orig.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Mode.Preferred != "socks5" {
		t.Errorf("mode.preferred: %q != socks5", loaded.Mode.Preferred)
	}
	if len(loaded.Tunnels) != 2 || loaded.Tunnels[0].Username != "user1" {
		t.Errorf("туннели после roundtrip не совпали: %+v", loaded.Tunnels)
	}
	if got, want := loaded.Tunnels[1].Routes, orig.Tunnels[1].Routes; len(got) != len(want) {
		t.Errorf("маршруты не сохранились: %v != %v", got, want)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "нет-такого.toml")); err == nil {
		t.Error("Load несуществующего файла должен давать ошибку")
	}
}

func TestValidateErrors(t *testing.T) {
	base := func() *Config {
		cfg, err := FromTOML([]byte(testTemplate))
		if err != nil {
			t.Fatalf("FromTOML: %v", err)
		}
		return cfg
	}

	cases := []struct {
		name    string
		mutate  func(*Config)
		wantSub string // подстрока ожидаемой ошибки
	}{
		{"неизвестный режим", func(c *Config) { c.Mode.Preferred = "wireguard" }, "mode.preferred"},
		{"пустое имя", func(c *Config) { c.Tunnels[0].Name = "" }, "пустое имя"},
		{"пустой endpoint", func(c *Config) { c.Tunnels[1].Endpoint = "" }, "пустой endpoint"},
		{"нулевой порт", func(c *Config) { c.Tunnels[0].SocksPort = 0 }, "socks_port"},
		{"порт вне диапазона", func(c *Config) { c.Tunnels[0].SocksPort = 70000 }, "socks_port"},
		{"дубликат порта", func(c *Config) { c.Tunnels[1].SocksPort = c.Tunnels[0].SocksPort }, "один socks_port"},
		// Имя — идентификатор туннеля: одноимённые затирали бы друг друга
		// в менеджере, и один из них молча исчезал бы из работы.
		{"дубликат имени", func(c *Config) { c.Tunnels[1].Name = c.Tunnels[0].Name }, "именем"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("ожидалась ошибка валидации")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("ошибка %q не содержит %q", err, tc.wantSub)
			}
		})
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := Default()
	cfg.Mode.Preferred = "плохой"
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Error("Load невалидного конфига должен давать ошибку")
	}
}
