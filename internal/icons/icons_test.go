package icons

import (
	"bytes"
	"encoding/binary"
	"runtime"
	"testing"
)

// TestPNGValid — эмблема в PNG должна быть непустой и иметь корректную
// сигнатуру: пустой или битый файл иначе всплыл бы только визуально,
// пропавшей иконкой в трее.
func TestPNGValid(t *testing.T) {
	data := PNG()
	if len(data) == 0 {
		t.Fatal("appicon.png пуст — иконка не сгенерирована (go run build/icon/main.go)")
	}
	signature := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if !bytes.HasPrefix(data, signature) {
		t.Errorf("appicon.png не в формате PNG: % x", data[:min(8, len(data))])
	}
}

// TestTrayFormat — systray на Windows принимает только ICO, на остальных
// платформах — PNG. Формат не того вида означает иконку трея, которой не
// видно.
func TestTrayFormat(t *testing.T) {
	data := Tray()
	if len(data) == 0 {
		t.Fatal("иконка трея пуста")
	}
	if runtime.GOOS != "windows" {
		if !bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G'}) {
			t.Errorf("вне Windows иконка трея должна быть PNG: % x", data[:min(4, len(data))])
		}
		return
	}

	// Заголовок ICO: reserved=0, тип=1 (иконка), число картинок > 0.
	if len(data) < 6 {
		t.Fatalf("ICO короче заголовка: %d байт", len(data))
	}
	le := binary.LittleEndian
	if r := le.Uint16(data[0:2]); r != 0 {
		t.Errorf("ICO: поле reserved = %d, ожидалось 0", r)
	}
	if typ := le.Uint16(data[2:4]); typ != 1 {
		t.Errorf("ICO: тип = %d, ожидалась иконка (1)", typ)
	}
	if n := le.Uint16(data[4:6]); n == 0 {
		t.Error("ICO не содержит ни одной картинки")
	}
}

// TestICOContainsSmallSize — в трее и заголовке окна Windows берёт мелкий
// размер. Если его нет, система масштабирует крупный, и иконка выглядит
// размытой.
func TestICOContainsSmallSize(t *testing.T) {
	data := ico
	le := binary.LittleEndian
	count := int(le.Uint16(data[4:6]))
	for i := 0; i < count; i++ {
		entry := 6 + 16*i
		if data[entry] == 16 { // ширина в первом байте записи каталога
			return
		}
	}
	t.Error("в ICO нет размера 16×16 — иконка в трее будет масштабированной")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
