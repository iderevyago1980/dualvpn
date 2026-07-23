// DualVPN — одновременное подключение к двум Cisco AnyConnect VPN.
//
// Точка входа Wails-приложения: загружает конфигурацию, создаёт менеджер
// туннелей и открывает desktop-окно с UI из каталога frontend/.
// Биндинги для JS — структура ui.App (window.go.ui.App).
package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"dualvpn/internal/ui"
)

// Статические файлы фронтенда встраиваются в бинарник.
//
//go:embed all:frontend
var assets embed.FS

func main() {
	app, err := ui.NewApp("config.toml")
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
		OnStartup:  app.Startup,   // запускает системный трей и трансляцию событий
		OnShutdown: app.Shutdown,  // останавливает трей и все туннели
		// Закрытие окна прячет приложение в трей; настоящий выход —
		// пункт «Выход» в меню трея (BeforeClose тогда вернёт false).
		OnBeforeClose: app.BeforeClose,
		Bind:          []interface{}{app},
	})
	if err != nil {
		log.Fatalf("wails: %v", err)
	}
}
