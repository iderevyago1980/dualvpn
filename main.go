// DualVPN — одновременное подключение к двум Cisco AnyConnect VPN.
//
// Точка входа Wails-приложения: загружает конфигурацию, создаёт менеджер
// туннелей и открывает desktop-окно с UI из каталога frontend/.
// Биндинги для JS — структура ui.App (window.go.ui.App).
package main

import (
	"embed"
	"log"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"

	"dualvpn/internal/config"
	"dualvpn/internal/icons"
	"dualvpn/internal/ui"
)

// Статические файлы фронтенда встраиваются в бинарник.
//
//go:embed all:frontend
var assets embed.FS

// Шаблон конфигурации: разворачивается при первом запуске, если файла
// настроек ещё нет. Встраивается именно config.example.toml, чтобы список
// эндпоинтов и групп жил в данных с комментариями, а не в коде.
//
//go:embed config.example.toml
var starterConfig []byte

func main() {
	app, err := ui.NewApp(configPath(), starterConfig)
	if err != nil {
		log.Fatalf("инициализация: %v", err)
	}

	err = wails.Run(&options.App{
		Title:     "DualVPN",
		Width:     1000,
		Height:    700,
		MinWidth:  800,
		MinHeight: 600,
		Frameless: false,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.Startup,  // запускает системный трей и трансляцию событий
		OnShutdown: app.Shutdown, // останавливает трей и все туннели
		// Закрытие окна прячет приложение в трей; настоящий выход —
		// пункт «Выход» в меню трея (BeforeClose тогда вернёт false).
		OnBeforeClose: app.BeforeClose,
		Bind:          []interface{}{app},
		// На Windows иконку окна и панели задач даёт ресурс, вшитый в exe
		// (rsrc_windows_amd64.syso); на Linux её нужно передать явно.
		Linux: &linux.Options{Icon: icons.PNG()},
	})
	if err != nil {
		log.Fatalf("wails: %v", err)
	}
}

// configPath выбирает путь к конфигу с приоритетами:
//  1. переменная окружения DUALVPN_CONFIG (явное переопределение);
//  2. локальный config.toml в рабочем каталоге, если он есть (удобно при
//     разработке — `go run .` из корня репозитория);
//  3. пользовательский каталог настроек ОС (~/.config/dualvpn/config.toml).
//
// Пункт 3 критичен для установленного пакета: при запуске из меню рабочий
// каталог — «/» или домашний, и относительный путь писать было бы некуда.
func configPath() string {
	if p := os.Getenv("DUALVPN_CONFIG"); p != "" {
		return p
	}
	if _, err := os.Stat("config.toml"); err == nil {
		return "config.toml"
	}
	if p, err := config.DefaultPath(); err == nil {
		return p
	}
	return "config.toml"
}
