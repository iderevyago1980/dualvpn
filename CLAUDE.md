# DualVPN — Project Context

## Project
DualVPN: Go application for simultaneous dual Cisco AnyConnect VPN connections.
Two modes: TUN (admin) and SOCKS5 (no-admin). Wails v3 UI.

## Environment
- Go 1.24 at /usr/local/go/bin/go (add to PATH)
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
- Go: export PATH="/usr/local/go/bin:$PATH" && go build ./...
- OpenConnect test: openconnect --version
