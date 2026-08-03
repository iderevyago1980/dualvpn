package ui

import (
	"encoding/json"
	"testing"

	"dualvpn/internal/config"
)

// newTunnel — запись, которую создаёт кнопка «+ Добавить туннель»
// во фронтенде (те же поля и порядок заполнения).
func newTunnel(name, endpoint string, port int) config.Tunnel {
	return config.Tunnel{
		Name:      name,
		Endpoint:  endpoint,
		Group:     "",
		Username:  "",
		Password:  "",
		SocksPort: port,
		TunName:   "dualvpn9",
		Routes:    []string{},
	}
}

// TestAddedTunnelIsRegistered — главная проверка добавления: фронтенд
// дописывает запись в конфигурацию и вызывает SaveConfig. Туннель обязан
// появиться не только в конфигурации, но и в менеджере — иначе кнопка
// «Подключить» для него ничего не сделает.
func TestAddedTunnelIsRegistered(t *testing.T) {
	app := newTestApp(t)
	cfg := app.GetConfig()
	before := len(cfg.Tunnels)

	cfg.Tunnels = append(cfg.Tunnels, newTunnel("Третий", "vpn3.example.test", 1082))
	if err := app.SaveConfig(*cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	if got := len(app.GetTunnels()); got != before+1 {
		t.Errorf("туннелей в конфигурации %d, ожидалось %d", got, before+1)
	}
	// Неизвестный менеджеру туннель отдаёт пустой режим.
	if st := app.GetTunnelStatus("Третий"); st.Mode == "" {
		t.Error("добавленный туннель не зарегистрирован в менеджере — подключить его нельзя")
	}
}

// TestAddedTunnelSurvivesReload — добавленный туннель должен пережить
// перезапуск приложения: SaveConfig обязан записать его на диск, а не
// только в память.
func TestAddedTunnelSurvivesReload(t *testing.T) {
	app := newTestApp(t)
	cfg := app.GetConfig()
	cfg.Tunnels = append(cfg.Tunnels, newTunnel("Третий", "vpn3.example.test", 1082))
	if err := app.SaveConfig(*cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	// Тот же путь к конфигурации — как при следующем запуске приложения.
	reloaded, err := NewApp(app.cfgPath, nil)
	if err != nil {
		t.Fatalf("повторный запуск: %v", err)
	}
	found := false
	for _, tun := range reloaded.GetTunnels() {
		if tun.Name == "Третий" && tun.Endpoint == "vpn3.example.test" {
			found = true
		}
	}
	if !found {
		t.Error("добавленный туннель не сохранился на диск")
	}
	if st := reloaded.GetTunnelStatus("Третий"); st.Mode == "" {
		t.Error("после перезапуска туннель не зарегистрирован в менеджере")
	}
}

// TestDeletedTunnelIsUnregistered — удаление убирает туннель и из
// конфигурации, и из менеджера: иначе он остался бы подключаемым.
func TestDeletedTunnelIsUnregistered(t *testing.T) {
	app := newTestApp(t)
	cfg := app.GetConfig()
	if len(cfg.Tunnels) < 2 {
		t.Fatalf("ожидалось не меньше двух туннелей в шаблоне, есть %d", len(cfg.Tunnels))
	}
	removed := cfg.Tunnels[0].Name

	cfg.Tunnels = cfg.Tunnels[1:]
	if err := app.SaveConfig(*cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	if st := app.GetTunnelStatus(removed); st.Mode != "" {
		t.Errorf("удалённый туннель %q всё ещё зарегистрирован в менеджере", removed)
	}
	for _, tun := range app.GetTunnels() {
		if tun.Name == removed {
			t.Errorf("удалённый туннель %q остался в конфигурации", removed)
		}
	}
}

// TestAddTunnelRejectsConflicts — конфликты должны возвращаться ошибкой,
// а не портить рабочую конфигурацию: фронтенд показывает её в подвале.
func TestAddTunnelRejectsConflicts(t *testing.T) {
	cases := []struct {
		name   string
		tunnel config.Tunnel
	}{
		{"пустой адрес сервера", newTunnel("Третий", "", 1082)},
		{"занятое имя", newTunnel("Первый", "vpn3.example.test", 1082)},
		{"занятый порт", newTunnel("Третий", "vpn3.example.test", 1080)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp(t)
			cfg := app.GetConfig()
			before := len(cfg.Tunnels)

			cfg.Tunnels = append(cfg.Tunnels, tc.tunnel)
			if err := app.SaveConfig(*cfg); err == nil {
				t.Fatal("SaveConfig принял конфликтующий туннель")
			}
			if got := len(app.GetTunnels()); got != before {
				t.Errorf("после отказа туннелей стало %d, было %d — конфигурация испорчена", got, before)
			}
		})
	}
}

// TestTunnelFieldNamesForFrontend — фронтенд создаёт новый туннель как
// объект с этими именами полей (frontend/app.js, addTunnel). Wails
// сериализует структуру через encoding/json, поэтому переименование поля
// или добавление json-тега молча сломает добавление туннелей.
func TestTunnelFieldNamesForFrontend(t *testing.T) {
	data, err := json.Marshal(config.Tunnel{})
	if err != nil {
		t.Fatalf("сериализация: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("разбор: %v", err)
	}

	for _, want := range []string{"Name", "Endpoint", "Group", "Username", "Password", "SocksPort", "TunName", "Routes"} {
		if _, ok := fields[want]; !ok {
			t.Errorf("поле %q отсутствует — фронтенд заполняет именно его", want)
		}
	}
}
