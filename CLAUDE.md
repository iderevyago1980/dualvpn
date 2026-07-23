# DualVPN — Project Context

## Project
DualVPN: Go application for simultaneous dual Cisco AnyConnect VPN connections.
Two modes: TUN (admin) and SOCKS5 (no-admin). Wails v2 UI (v2.13.0, ~/go/bin/wails).

## Environment
- Go 1.26.5 at /usr/local/go/bin/go (add to PATH)
- OpenConnect v9.12 at /usr/sbin/openconnect
- ocproxy at /usr/bin/ocproxy
- Claude Code v2.1.199 at ~/.local/bin/claude

## Endpoints (verified)
1. vpn2.astralinux.ru — Cisco ASA, AnyConnect SSL/TLS, groups: Basic 2FA / Astra2FA Partners / Basic / Partners Astralinux / AstraLinuxExt
2. vpn.mt-integration.ru — Cisco ASA, AnyConnect SSL/TLS, groups: MT-I_RA / MT-I_RA_MFA / MT-I_RA_no_split

## Key Files
- SPEC.md — full technical specification
- README.md — project overview

## Commands
- Go: export PATH="/usr/local/go/bin:$PATH" GOFLAGS="-tags=webkit2_41" && go build ./...
  (тег webkit2_41 обязателен: в системе только libwebkit2gtk-4.1, без тега Wails ищет 4.0)
- Wails build: wails build -tags webkit2_41 (НЕ запускать wails dev — на сервере нет GUI)
- OpenConnect test: openconnect --version
