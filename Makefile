# DualVPN — сборка под Linux и Windows.
#
# Для Linux обязателен тег webkit2_41: в системе только libwebkit2gtk-4.1.
# Windows-бинарник собирается кросс-компиляцией без cgo (Wails на Windows
# использует чистый Go WebView2-загрузчик, systray — winapi).

GO      ?= go
WAILS   ?= wails
BINDIR  := bin

LINUX_TAGS   := desktop,production,webkit2_41
WINDOWS_TAGS := desktop,production

.PHONY: build-linux build-windows build-all test clean dev build

# Linux-бинарник (нужны libwebkit2gtk-4.1-dev и libayatana-appindicator3-dev).
build-linux:
	GOOS=linux GOARCH=amd64 $(GO) build -tags $(LINUX_TAGS) -o $(BINDIR)/dualvpn-linux

# Windows-бинарник: GUI-подсистема (без консоли), урезанные symbols/DWARF.
build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -tags $(WINDOWS_TAGS) \
		-ldflags "-H=windowsgui -s -w" -o $(BINDIR)/DualVPN.exe

build-all: build-linux build-windows

test:
	$(GO) test ./internal/... -v

clean:
	rm -rf $(BINDIR)/

# Не запускать на сервере без GUI.
dev:
	$(WAILS) dev

build:
	$(WAILS) build -clean -platform windows/amd64
