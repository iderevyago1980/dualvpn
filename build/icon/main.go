//go:build ignore

// Генератор иконок DualVPN.
//
// Рисует ту же эмблему, что и build/linux/dualvpn.svg (два перекрывающихся
// щита-туннеля с замком), и раскладывает её в файлы, которые нужны сборке:
//
//	build/appicon.png        — 256×256, источник для упаковки (.deb, ярлыки)
//	build/windows/icon.ico   — многоразмерная иконка для exe и установщика
//	internal/icons/*         — те же файлы, встраиваемые в бинарь для трея
//
// Иконка генерируется, а не хранится картинкой, чтобы эмблема оставалась
// одной и той же во всех местах и её можно было пересобрать после правки.
//
// Запуск: go run build/icon/main.go
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"

	"golang.org/x/image/vector"
)

// canvas — система координат эмблемы (совпадает с viewBox SVG).
const canvas = 256.0

// icoSizes — размеры, которые Windows выбирает под контекст: 16 — трей и
// заголовок окна, 32 — панель задач, 256 — крупная плитка в проводнике.
var icoSizes = []int{16, 24, 32, 48, 64, 128, 256}

func main() {
	root, err := repoRoot()
	if err != nil {
		log.Fatal(err)
	}

	// PNG 256×256 — для упаковки и для трея на Linux.
	png256 := render(256)
	// Многоразмерный ICO — иконка exe, ярлыков, установщика и трея Windows.
	images := make([]*image.RGBA, 0, len(icoSizes))
	for _, s := range icoSizes {
		images = append(images, render(s))
	}

	// internal/icons — те же файлы, но встраиваемые в бинарь (go:embed
	// умеет брать файлы только из своего пакета, поэтому нужна копия).
	for _, dir := range []string{
		filepath.Join(root, "build"),
		filepath.Join(root, "internal", "icons"),
	} {
		p := filepath.Join(dir, "appicon.png")
		if err := writePNG(p, png256); err != nil {
			log.Fatalf("%s: %v", p, err)
		}
		fmt.Println("готово:", p)
	}
	for _, p := range []string{
		filepath.Join(root, "build", "windows", "icon.ico"),
		filepath.Join(root, "internal", "icons", "icon.ico"),
	} {
		if err := writeICO(p, images); err != nil {
			log.Fatalf("%s: %v", p, err)
		}
		fmt.Println("готово:", p)
	}
}

// render рисует эмблему заданного размера.
func render(size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	k := float64(size) / canvas

	// Подложка: тёмный скруглённый квадрат с вертикальным градиентом.
	bg := newRasterizer(size)
	roundedRect(bg, 8*k, 8*k, 240*k, 240*k, 52*k)
	fill(dst, bg, verticalGradient(size,
		color.RGBA{0x1f, 0x29, 0x37, 0xff},
		color.RGBA{0x0f, 0x17, 0x2a, 0xff}))

	// Левый щит — голубой, правый — зелёный (перекрывает левый).
	left := newRasterizer(size)
	shield(left, 96*k, k)
	fill(dst, left, diagonalGradient(size,
		color.RGBA{0x38, 0xbd, 0xf8, 0xff},
		color.RGBA{0x0e, 0xa5, 0xe9, 0xff}, 0.92))

	right := newRasterizer(size)
	shield(right, 160*k, k)
	fill(dst, right, diagonalGradient(size,
		color.RGBA{0x34, 0xd3, 0x99, 0xff},
		color.RGBA{0x10, 0xb9, 0x81, 0xff}, 0.92))

	// Замок в зоне перекрытия — тем же тёмным цветом, что и подложка.
	lockR := newRasterizer(size)
	lock(lockR, k)
	fill(dst, lockR, solid(size, color.RGBA{0x0f, 0x17, 0x2a, 0xff}))

	return dst
}

func newRasterizer(size int) *vector.Rasterizer {
	return vector.NewRasterizer(size, size)
}

// fill заливает накопленный в растеризаторе контур источником src.
func fill(dst *image.RGBA, r *vector.Rasterizer, src image.Image) {
	r.Draw(dst, dst.Bounds(), src, image.Point{})
}

// roundedRect добавляет в контур скруглённый прямоугольник.
func roundedRect(r *vector.Rasterizer, x, y, w, h, rad float64) {
	r.MoveTo(f(x+rad), f(y))
	r.LineTo(f(x+w-rad), f(y))
	arc(r, x+w-rad, y+rad, rad, -90, 0)
	r.LineTo(f(x+w), f(y+h-rad))
	arc(r, x+w-rad, y+h-rad, rad, 0, 90)
	r.LineTo(f(x+rad), f(y+h))
	arc(r, x+rad, y+h-rad, rad, 90, 180)
	r.LineTo(f(x), f(y+rad))
	arc(r, x+rad, y+rad, rad, 180, 270)
	r.ClosePath()
}

// shield добавляет контур щита-туннеля. cx — координата его вершины по X
// в системе SVG (96 для левого щита, 160 для правого), k — масштаб.
//
// Форма повторяет путь из dualvpn.svg: скошенная «крыша», прямые борта и
// сходящееся книзу основание из двух кубических кривых.
func shield(r *vector.Rasterizer, cx, k float64) {
	left := cx - 48*k  // борт слева от вершины
	right := cx + 48*k // борт справа
	top := 60 * k
	shoulder := 80 * k
	waist := 120 * k
	bottom := 192 * k

	r.MoveTo(f(cx), f(top))
	r.LineTo(f(left), f(shoulder))
	r.LineTo(f(left), f(waist))
	// Низ щита: от левого борта к острию и обратно к правому борту.
	r.CubeTo(
		f(left), f(waist+34*k),
		f(left+22*k), f(waist+58*k),
		f(cx), f(bottom))
	r.CubeTo(
		f(cx+26*k), f(bottom-14*k),
		f(right), f(bottom-38*k),
		f(right), f(waist))
	r.LineTo(f(right), f(shoulder))
	r.ClosePath()
}

// lock добавляет контур навесного замка: корпус и дужка.
func lock(r *vector.Rasterizer, k float64) {
	// Корпус — скруглённый прямоугольник (в SVG: x=-20 y=-2 w=40 h=34 rx=7
	// со смещением translate(128,132)).
	roundedRect(r, 108*k, 130*k, 40*k, 34*k, 7*k)

	// Дужка — полукольцо над корпусом: внешний полукруг радиусом 13 и
	// внутренний радиусом 4, соединённые прямыми участками.
	cx, cy := 128*k, 121*k
	outer, inner := 13*k, 4*k
	r.MoveTo(f(cx-outer), f(cy+9*k))
	r.LineTo(f(cx-outer), f(cy))
	arc(r, cx, cy, outer, 180, 360)
	r.LineTo(f(cx+outer), f(cy+9*k))
	r.LineTo(f(cx+inner), f(cy+9*k))
	r.LineTo(f(cx+inner), f(cy))
	arc(r, cx, cy, inner, 360, 180)
	r.LineTo(f(cx-inner), f(cy+9*k))
	r.ClosePath()
}

// arc добавляет дугу окружности отрезками. Углы в градусах, ось Y смотрит
// вниз (как в SVG). Шаг подобран так, чтобы на 256 px излом был незаметен.
func arc(r *vector.Rasterizer, cx, cy, rad, from, to float64) {
	const step = 4.0 // градусов
	n := int(math.Abs(to-from)/step) + 1
	for i := 1; i <= n; i++ {
		t := (from + (to-from)*float64(i)/float64(n)) * math.Pi / 180
		r.LineTo(f(cx+rad*math.Cos(t)), f(cy+rad*math.Sin(t)))
	}
}

func f(v float64) float32 { return float32(v) }

// solid — источник сплошного цвета.
func solid(size int, c color.RGBA) image.Image {
	return image.NewUniform(c)
}

// verticalGradient — вертикальный градиент от top к bottom.
func verticalGradient(size int, top, bottom color.RGBA) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		t := float64(y) / float64(size-1)
		c := mix(top, bottom, t, 1)
		for x := 0; x < size; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// diagonalGradient — градиент по диагонали (как x1=0,y1=0 → x2=1,y2=1 в SVG)
// с общей прозрачностью alpha.
func diagonalGradient(size int, from, to color.RGBA, alpha float64) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			t := (float64(x) + float64(y)) / float64(2*(size-1))
			img.SetRGBA(x, y, mix(from, to, t, alpha))
		}
	}
	return img
}

// mix смешивает два цвета и возвращает цвет с умноженной альфой в
// premultiplied-виде, который ожидает image/draw.
func mix(a, b color.RGBA, t, alpha float64) color.RGBA {
	lerp := func(x, y uint8) float64 { return float64(x) + (float64(y)-float64(x))*t }
	return color.RGBA{
		R: uint8(lerp(a.R, b.R) * alpha),
		G: uint8(lerp(a.G, b.G) * alpha),
		B: uint8(lerp(a.B, b.B) * alpha),
		A: uint8(255 * alpha),
	}
}

func writePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	fh, err := os.Create(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	return png.Encode(fh, img)
}

// writeICO собирает многоразмерный ICO: каждая картинка хранится внутри как
// PNG (Windows понимает такой контейнер начиная с Vista).
func writeICO(path string, images []*image.RGBA) error {
	var body bytes.Buffer
	type entry struct{ size, length, offset int }
	entries := make([]entry, 0, len(images))

	// Данные всех картинок идут после заголовка и каталога.
	offset := 6 + 16*len(images)
	for _, img := range images {
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return err
		}
		entries = append(entries, entry{
			size:   img.Bounds().Dx(),
			length: buf.Len(),
			offset: offset,
		})
		offset += buf.Len()
		body.Write(buf.Bytes())
	}

	var out bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&out, le, uint16(0)) // reserved
	_ = binary.Write(&out, le, uint16(1)) // тип: иконка
	_ = binary.Write(&out, le, uint16(len(images)))
	for _, e := range entries {
		dim := byte(e.size)
		if e.size >= 256 {
			dim = 0 // 256 обозначается нулём
		}
		out.Write([]byte{dim, dim, 0, 0})
		_ = binary.Write(&out, le, uint16(1))  // цветовых плоскостей
		_ = binary.Write(&out, le, uint16(32)) // бит на пиксель
		_ = binary.Write(&out, le, uint32(e.length))
		_ = binary.Write(&out, le, uint32(e.offset))
	}
	out.Write(body.Bytes())

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}

// repoRoot возвращает корень репозитория относительно этого файла
// (build/icon/main.go), чтобы генератор не зависел от рабочего каталога.
func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		if filepath.Dir(dir) == dir {
			return "", fmt.Errorf("не найден корень репозитория (go.mod) от %s", wd)
		}
	}
}
