package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dualvpn/internal/config"
	"dualvpn/internal/mode"
)

// starterTemplate — шаблон конфигурации для тестов. В бою сюда попадает
// встроенный config.example.toml; в тестах эндпоинты фиктивные, чтобы
// проверки не зависели от содержимого боевого примера.
const starterTemplate = `[mode]
preferred = "auto"

[[tunnels]]
name = "Первый"
endpoint = "vpn1.example.test"
group = "Группа A"
socks_port = 1080
tun_name = "tun-a"

[[tunnels]]
name = "Второй"
endpoint = "vpn2.example.test"
group = "Группа B"
socks_port = 1081
tun_name = "tun-b"
`

// newTestApp создаёт App поверх временного конфига. ctx остаётся nil —
// значит методы с runtime.* (EventsEmit/Window*) пропускают вызов во
// фронтенд, и тесты не требуют Wails-рантайма и дисплея.
func newTestApp(t *testing.T) *App {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	app, err := NewApp(path, []byte(starterTemplate))
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	return app
}

// --- resolveMode: чистая логика разрешения режима из конфига ---

func TestResolveMode(t *testing.T) {
	if got := resolveMode("tun"); got != "tun" {
		t.Errorf("resolveMode(\"tun\") = %q, ожидалось \"tun\"", got)
	}
	if got := resolveMode("socks5"); got != "socks5" {
		t.Errorf("resolveMode(\"socks5\") = %q, ожидалось \"socks5\"", got)
	}
	// "auto" и пустая строка → автодетекция.
	if got := resolveMode("auto"); got != mode.Detect() {
		t.Errorf("resolveMode(\"auto\") = %q, ожидалось %q", got, mode.Detect())
	}
	if got := resolveMode(""); got != mode.Detect() {
		t.Errorf("resolveMode(\"\") = %q, ожидалось %q", got, mode.Detect())
	}
}

// --- loadOrCreate: создание дефолта, загрузка, отклонение битого ---

func TestLoadOrCreateCreatesDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg, err := loadOrCreate(path, []byte(starterTemplate))
	if err != nil {
		t.Fatalf("loadOrCreate: %v", err)
	}
	if len(cfg.Tunnels) != 2 {
		t.Errorf("из шаблона получено %d туннелей, ожидалось 2", len(cfg.Tunnels))
	}
	// Файл должен быть создан на диске — повторная загрузка читает его же.
	if _, err := config.Load(path); err != nil {
		t.Errorf("созданный конфиг не читается обратно: %v", err)
	}
}

// Шаблон разворачивается дословно: комментарии в созданном файле должны
// сохраниться, иначе пользователь потеряет подсказки по группам и режимам.
func TestLoadOrCreateKeepsTemplateComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	tpl := "# подсказка\n" + starterTemplate
	if _, err := loadOrCreate(path, []byte(tpl)); err != nil {
		t.Fatalf("loadOrCreate: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "# подсказка") {
		t.Error("комментарии шаблона не попали в созданный конфиг")
	}
}

// Без шаблона конфиг создаётся пустым — эндпоинтов в коде больше нет.
func TestLoadOrCreateWithoutTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg, err := loadOrCreate(path, nil)
	if err != nil {
		t.Fatalf("loadOrCreate: %v", err)
	}
	if len(cfg.Tunnels) != 0 {
		t.Errorf("без шаблона получено %d туннелей, ожидалось 0", len(cfg.Tunnels))
	}
}

func TestLoadOrCreateLoadsExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	want, err := config.FromTOML([]byte(starterTemplate))
	if err != nil {
		t.Fatalf("FromTOML: %v", err)
	}
	want.Tunnels = want.Tunnels[:1]
	want.Tunnels[0].Name = "OnlyOne"
	if err := want.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := loadOrCreate(path, []byte(starterTemplate))
	if err != nil {
		t.Fatalf("loadOrCreate: %v", err)
	}
	if len(got.Tunnels) != 1 || got.Tunnels[0].Name != "OnlyOne" {
		t.Errorf("loadOrCreate вернул не сохранённый конфиг: %+v", got.Tunnels)
	}
}

func TestLoadOrCreateRejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	// Существующий, но невалидный файл (недопустимый mode.preferred).
	bad := config.Default()
	bad.Mode.Preferred = "auto"
	if err := bad.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Портим уже на диске через прямую запись невалидного TOML.
	if err := os.WriteFile(path, []byte("[mode]\npreferred = \"nope\"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := loadOrCreate(path, []byte(starterTemplate)); err == nil {
		t.Error("loadOrCreate принял конфиг с mode.preferred=\"nope\", ожидалась ошибка")
	}
}

// --- NewApp + геттеры ---

func TestNewAppRegistersDefaults(t *testing.T) {
	app := newTestApp(t)
	if n := len(app.GetTunnels()); n != 2 {
		t.Errorf("GetTunnels вернул %d, ожидалось 2", n)
	}
	if m := app.GetMode(); m != mode.TUN && m != mode.SOCKS5 {
		t.Errorf("GetMode = %q, ожидался tun|socks5", m)
	}
}

func TestGetTunnelsReturnsCopy(t *testing.T) {
	app := newTestApp(t)
	ts := app.GetTunnels()
	if len(ts) == 0 {
		t.Fatal("нет туннелей")
	}
	ts[0].Name = "ПОДМЕНА"
	if app.GetTunnels()[0].Name == "ПОДМЕНА" {
		t.Error("GetTunnels отдаёт ссылку на внутренний срез — мутация протекла в App")
	}
}

// --- SwitchMode: валидация и переключение ---

func TestSwitchModeUnknown(t *testing.T) {
	app := newTestApp(t)
	before := app.GetMode()
	if err := app.SwitchMode("bogus"); err == nil {
		t.Error("SwitchMode(\"bogus\") не вернул ошибку")
	}
	if app.GetMode() != before {
		t.Error("SwitchMode с ошибкой всё равно сменил режим")
	}
}

func TestSwitchModeSocks5(t *testing.T) {
	app := newTestApp(t)
	if err := app.SwitchMode(mode.SOCKS5); err != nil {
		t.Fatalf("SwitchMode(socks5): %v", err)
	}
	if app.GetMode() != mode.SOCKS5 {
		t.Errorf("GetMode = %q после SwitchMode(socks5)", app.GetMode())
	}
}

func TestSwitchModeAuto(t *testing.T) {
	app := newTestApp(t)
	if err := app.SwitchMode("auto"); err != nil {
		t.Fatalf("SwitchMode(auto): %v", err)
	}
	if app.GetMode() != mode.Detect() {
		t.Errorf("SwitchMode(auto) → %q, ожидалось %q", app.GetMode(), mode.Detect())
	}
}

func TestSwitchModeTUNRequiresAdmin(t *testing.T) {
	app := newTestApp(t)
	err := app.SwitchMode(mode.TUN)
	if mode.IsAdmin() {
		if err != nil {
			t.Errorf("под админом SwitchMode(tun) вернул ошибку: %v", err)
		}
		return
	}
	// Не админ: tun запрещён.
	if err == nil {
		t.Fatal("без прав администратора SwitchMode(tun) должен вернуть ошибку")
	}
	if !strings.Contains(err.Error(), "администратор") {
		t.Errorf("ошибка не про права администратора: %v", err)
	}
	if app.GetMode() == mode.TUN {
		t.Error("режим переключился в tun несмотря на отказ")
	}
}

// --- SaveConfig: валидация и запись ---

func TestSaveConfigValid(t *testing.T) {
	app := newTestApp(t)
	cfg := *app.GetConfig()
	cfg.Tunnels[0].Name = "Переименован"
	if err := app.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if app.GetConfig().Tunnels[0].Name != "Переименован" {
		t.Error("SaveConfig не обновил конфиг в App")
	}
	// Изменение должно уйти на диск.
	onDisk, err := config.Load(app.cfgPath)
	if err != nil {
		t.Fatalf("Load с диска: %v", err)
	}
	if onDisk.Tunnels[0].Name != "Переименован" {
		t.Error("SaveConfig не записал изменение на диск")
	}
}

func TestSaveConfigInvalidRejected(t *testing.T) {
	app := newTestApp(t)
	orig := app.GetConfig().Tunnels[0].Name

	bad := *app.GetConfig()
	// Дублируем порт — Validate обязан отвергнуть.
	bad.Tunnels[1].SocksPort = bad.Tunnels[0].SocksPort
	if err := app.SaveConfig(bad); err == nil {
		t.Fatal("SaveConfig принял конфиг с дублирующимся socks_port")
	}
	// Конфиг в App не должен измениться.
	if app.GetConfig().Tunnels[0].Name != orig {
		t.Error("отклонённый SaveConfig всё же поменял конфиг в App")
	}
}

// --- Журнал: кольцевой буфер и копия ---

func TestLogRingBuffer(t *testing.T) {
	app := newTestApp(t)
	total := maxLogEntries + 50
	for i := 0; i < total; i++ {
		app.log("info", "msg")
	}
	logs := app.GetLogs()
	if len(logs) != maxLogEntries {
		t.Errorf("в журнале %d записей, ожидался предел %d", len(logs), maxLogEntries)
	}
}

func TestGetLogsReturnsCopy(t *testing.T) {
	app := newTestApp(t)
	app.log("info", "первая")
	logs := app.GetLogs()
	if len(logs) == 0 {
		t.Fatal("журнал пуст")
	}
	logs[0].Message = "ПОДМЕНА"
	if app.GetLogs()[0].Message == "ПОДМЕНА" {
		t.Error("GetLogs отдаёт ссылку на внутренний буфер")
	}
}

// --- Делегирование в менеджер: пути ошибок без сети ---

func TestConnectTunnelUnregistered(t *testing.T) {
	app := newTestApp(t)
	if err := app.ConnectTunnel("нет-такого"); err == nil {
		t.Error("ConnectTunnel для незарегистрированного туннеля не вернул ошибку")
	}
}

func TestDisconnectTunnel(t *testing.T) {
	app := newTestApp(t)
	// Незарегистрированный — ошибка.
	if err := app.DisconnectTunnel("нет-такого"); err == nil {
		t.Error("DisconnectTunnel для неизвестного id не вернул ошибку")
	}
	// Зарегистрированный, но не запущенный — остановка без ошибки.
	id := app.GetTunnels()[0].Name
	if err := app.DisconnectTunnel(id); err != nil {
		t.Errorf("DisconnectTunnel незапущенного туннеля: %v", err)
	}
}

func TestSubmit2FAErrors(t *testing.T) {
	app := newTestApp(t)
	if err := app.Submit2FA("нет-такого", "123456"); err == nil {
		t.Error("Submit2FA для неизвестного id не вернул ошибку")
	}
	// Зарегистрирован, но не запущен — код передавать некому.
	id := app.GetTunnels()[0].Name
	if err := app.Submit2FA(id, "123456"); err == nil {
		t.Error("Submit2FA незапущенному туннелю не вернул ошибку")
	}
}

func TestGetTunnelStatusUnknown(t *testing.T) {
	app := newTestApp(t)
	st := app.GetTunnelStatus("нет-такого")
	if st.Connected {
		t.Error("неизвестный туннель отмечен connected")
	}
}

// --- Прочее ---

func TestBeforeCloseQuittingAllowsClose(t *testing.T) {
	app := newTestApp(t)
	app.mu.Lock()
	app.quitting = true
	app.mu.Unlock()
	// Путь quitting не трогает runtime.* — ctx может быть nil.
	if app.BeforeClose(nil) {
		t.Error("BeforeClose при quitting=true вернул true (закрытие отменено)")
	}
}

func TestDetectModeAndIsAdminDelegate(t *testing.T) {
	app := newTestApp(t)
	if app.DetectMode() != mode.Detect() {
		t.Errorf("DetectMode = %q, ожидалось %q", app.DetectMode(), mode.Detect())
	}
	if app.IsAdmin() != mode.IsAdmin() {
		t.Errorf("IsAdmin = %v, ожидалось %v", app.IsAdmin(), mode.IsAdmin())
	}
}
