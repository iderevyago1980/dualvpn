// Package icons — эмблема DualVPN, встроенная в исполняемый файл.
//
// Файлы генерируются командой `go run build/icon/main.go` из той же
// эмблемы, что и иконка exe, ярлыков и установщика (см. build/icon/main.go).
// Правки вносятся в генератор, а не в картинки.
package icons

import (
	_ "embed"
	"runtime"
)

//go:embed icon.ico
var ico []byte

//go:embed appicon.png
var png []byte

// Tray возвращает иконку системного трея в формате, который принимает
// платформа: Windows — ICO (systray там понимает только его), остальные — PNG.
func Tray() []byte {
	if runtime.GOOS == "windows" {
		return ico
	}
	return png
}

// PNG возвращает эмблему в формате PNG (окно приложения на Linux).
func PNG() []byte { return png }
