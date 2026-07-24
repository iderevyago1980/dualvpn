package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("конфиг по умолчанию должен быть валиден: %v", err)
	}
	if len(cfg.Tunnels) != 2 {
		t.Errorf("ожидалось 2 туннеля, получено %d", len(cfg.Tunnels))
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.toml") // каталог создаётся Save
	orig := Default()
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
	base := func() *Config { return Default() }

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
