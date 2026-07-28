# DualVPN — сборка под Linux и Windows.
#
# Для Linux обязателен тег webkit2_41: в системе только libwebkit2gtk-4.1.
# Windows-бинарник собирается кросс-компиляцией без cgo (Wails на Windows
# использует чистый Go WebView2-загрузчик, systray — winapi).

GO       ?= go
WAILS    ?= wails
MAKENSIS ?= makensis
BINDIR   := bin
VERSION  ?= 1.9.0

LINUX_TAGS   := desktop,production,webkit2_41
WINDOWS_TAGS := desktop,production

# Версия показывается в интерфейсе; без этого флага сборка помечается как "dev".
VERSION_LDFLAG := -X dualvpn/internal/ui.version=$(VERSION)

DEB_ARCH := amd64
DEB_ROOT := $(BINDIR)/deb/dualvpn_$(VERSION)_$(DEB_ARCH)
DEB_PKG  := $(BINDIR)/dualvpn_$(VERSION)_$(DEB_ARCH).deb

.PHONY: build-linux build-windows build-all installer deb test clean dev build e2e

# Linux-бинарник (нужны libwebkit2gtk-4.1-dev и libayatana-appindicator3-dev).
build-linux:
	GOOS=linux GOARCH=amd64 $(GO) build -tags $(LINUX_TAGS) -o $(BINDIR)/dualvpn-linux

# Windows-бинарник: GUI-подсистема (без консоли), урезанные symbols/DWARF.
build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -tags $(WINDOWS_TAGS) \
		-ldflags "-H=windowsgui -s -w" -o $(BINDIR)/DualVPN.exe

build-all: build-linux build-windows

# Windows-инсталлятор (NSIS). Кросс-собирается на Linux через makensis.
# Требует свежий bin/DualVPN.exe (цель build-windows).
# Результат: bin/DualVPN-Setup-$(VERSION).exe
.PHONY: wintun
wintun: ## Скачать Wintun (wintun.dll) из релиза — бинарь не коммитится
	@build/windows/fetch-wintun.sh

installer: build-windows wintun
	$(MAKENSIS) -DAPPVERSION=$(VERSION) -DSRCROOT=$(CURDIR) build/windows/installer.nsi

# .deb-пакет для Debian/Ubuntu. Ключевое: секция Depends объявляет
# runtime-библиотеки (webkit2gtk-4.1, gtk3, ayatana-appindicator) — без них
# на чистой системе бинарник молча не стартует ("cannot open shared object
# file"). Плюс .desktop-ярлык с иконкой, чтобы приложение было в меню.
# Собирается без root: --root-owner-group проставляет владельца root:root.
# Имена gtk-пакета даны альтернативой (t64 для Ubuntu 24.04+, обычное — до).
deb: build-linux
	rm -rf $(DEB_ROOT)
	install -Dm755 $(BINDIR)/dualvpn-linux $(DEB_ROOT)/usr/bin/dualvpn
	install -Dm644 build/linux/dualvpn.desktop $(DEB_ROOT)/usr/share/applications/dualvpn.desktop
	install -Dm644 build/linux/dualvpn.svg $(DEB_ROOT)/usr/share/icons/hicolor/scalable/apps/dualvpn.svg
	mkdir -p $(DEB_ROOT)/DEBIAN
	sed -e 's/@VERSION@/$(VERSION)/' -e 's/@ARCH@/$(DEB_ARCH)/' \
		build/linux/control.in > $(DEB_ROOT)/DEBIAN/control
	dpkg-deb --root-owner-group --build $(DEB_ROOT) $(DEB_PKG)
	@echo "Готово: $(DEB_PKG)"

test:
	$(GO) test ./internal/... -v

# E2E host-стенд: ocserv (docker) + харнесс dualvpn-harness (SOCKS5, затем TUN
# через sudo) + curl-проверка изоляции. См. test/e2e/run.sh.
e2e: ## E2E host-стенд: ocserv + харнесс (SOCKS5 + TUN)
	@test/e2e/run.sh

.PHONY: e2e-vm
e2e-vm: ## E2E внутри настоящей Linux-VM (ocserv + QEMU-гость: .deb, harness, GUI-smoke)
	@test/e2e/vm/linux/run.sh

.PHONY: e2e-win-vm
e2e-win-vm: ## E2E внутри настоящей Windows 11 VM (autounattend + harness + GUI-smoke)
	@test/e2e/vm/windows/run-win.sh

clean:
	rm -rf $(BINDIR)/

# Не запускать на сервере без GUI.
dev:
	$(WAILS) dev

build:
	$(WAILS) build -clean -platform windows/amd64

.PHONY: e2e-win-ready
e2e-win-ready: ## Подтверждение DualVPN на Windows через готовый образ (work/srv.qcow2 + harness offline-инъекция)
	@test/e2e/vm/windows/run-ready.sh
