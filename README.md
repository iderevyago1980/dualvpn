# DualVPN

An application for connecting to two Cisco AnyConnect VPN endpoints at the same time.

## Architecture

- **Language**: Go
- **VPN protocol**: Cisco AnyConnect (SSL/TLS + DTLS) — a native Go client
  (a fork of [sslcon](https://github.com/tlslink/sslcon)); the external
  `openconnect` binary is **not** used
- **Two modes**: TUN (requires administrator rights) and SOCKS5 (no elevation)
- **UI**: Wails v2 (Go backend + HTML/CSS/JS frontend, system tray)

## Endpoints

Server addresses, tunnel-group names and internal subnets are not stored in this
repository — you set them in `config.toml` (see `config.example.toml`). The code and
the documentation use the placeholders `vpn1.example.com` / `vpn2.example.com`
throughout.

Cisco ASA and ocserv are supported over AnyConnect SSL/TLS, without SAML/SSO.
Connecting goes: pick a group → username → password → 2FA code (TOTP) if the server
asks for one.

The group name must match the alias on the server **literally**. The list of groups is
not baked into the application; it is requested from the server itself — either with the
"↻ from server" button next to the group field, or with `dualvpn-harness -groups`. An
empty group means "use the server's default group".

## Modes

### SOCKS5 (no administrator rights)
- gVisor netstack — a userspace TCP/IP stack
- Each tunnel exposes a local SOCKS5 proxy (1080, 1081)
- Applications are pointed at the proxy manually
- No TUN driver required
- Names are resolved by the gateway's DNS servers **inside the tunnel**; split-DNS
  zones never reach the system resolver

### TUN (with administrator rights)
- wintun.dll (Windows) / /dev/net/tun (Linux)
- Each tunnel creates its own TUN adapter
- Split tunneling through the route table
- Transparent to every application

### Auto-detection
- On startup the app checks whether it has administrator rights
- Elevated → TUN mode
- Not elevated → SOCKS5 mode
- The mode can be switched by hand in the UI; switching stops all tunnels. There is no
  automatic TUN→SOCKS5 fallback on error

## Project layout

```
dualvpn/
├── main.go                 # Entry point (embeds the frontend and config.example.toml)
├── go.mod
├── internal/
│   ├── config/             # TOML config, load/save
│   ├── vpn/                # Tunnel manager
│   │   └── sslcon/         # AnyConnect client: auth, 2FA, CSTP/DTLS
│   ├── socks5/             # SOCKS5 server + gVisor netstack + tunnel DNS
│   ├── tun/                # TUN adapters (wintun/tun)
│   ├── routing/            # Split-tunnel routes (netsh/route)
│   ├── mockasa/            # Cisco ASA emulator for tests
│   └── ui/                 # Wails frontend bindings
├── cmd/
│   ├── dualvpn-harness/    # Headless driver: connection, DNS, groups
│   └── dualvpn-tuncheck/   # TUN path self-check (requires administrator rights)
├── frontend/
│   ├── index.html          # UI (sidebar layout, dark theme)
│   ├── style.css
│   └── app.js
└── config.example.toml     # Configuration template (embedded into the binary)
```

`wintun.dll` is not committed to the repository: `make wintun`
(`build/windows/fetch-wintun.sh`) downloads it and places it next to the exe — the
driver is only loaded from the program directory or System32.

## Building and running on Windows

```bash
go build -tags desktop,production -ldflags "-H=windowsgui -s -w" -o bin/DualVPN.exe .
```

`wintun.dll` is only needed for TUN mode; SOCKS5 works without it and without
administrator rights.

**Smart App Control.** On Windows 11 with Smart App Control enabled, unsigned builds are
blocked by file reputation ("blocked by Device Guard policy"). For development
`go run ./cmd/...` helps; shipping to users requires a signing certificate.

### Signing builds

```powershell
build\windows\sign.ps1 -CreateSelfSigned -ExportCer bin\DualVPN-selfsigned.cer  # first time
build\windows\sign.ps1 -Thumbprint <thumbprint>                                 # afterwards
```

signtool.exe from the Windows SDK is not needed — the built-in
`Set-AuthenticodeSignature` does the signing. A timestamp is always applied: without one
the signature stops being considered valid the day the certificate expires.

Releases are signed with a **self-signed** certificate
(`CN=DualVPN (self-signed)`, thumbprint
`A97246B76B693EADE8DA3B99193C37F680FB53CB`). Windows does not know this root, so the
signature only counts as valid where the certificate has been installed by hand:

```powershell
Import-Certificate -FilePath .\DualVPN-selfsigned.cer -CertStoreLocation Cert:\CurrentUser\Root
Import-Certificate -FilePath .\DualVPN-selfsigned.cer -CertStoreLocation Cert:\CurrentUser\TrustedPublisher
```

That makes sense for deployment inside an organization (including via GPO) and is
useless for public distribution: Smart App Control and SmartScreen look at publisher
reputation rather than at the mere presence of a signature, and will block the file even
with a formally valid one. To get rid of the warnings for outside users you need a
commercial OV/EV certificate (since 2023 — only with a hardware token or a cloud HSM),
or Azure Trusted Signing.

## Diagnostics without the GUI

```bash
go run ./cmd/dualvpn-harness -config config.toml -groups                 # which groups the server offers
go run ./cmd/dualvpn-harness -config config.toml -otp 123456 -hold 30s   # bring tunnels up and hold them
go run ./cmd/dualvpn-harness -config config.toml -resolve host.corp.example   # check DNS inside the tunnel
go build -o bin/dualvpn-tuncheck.exe ./cmd/dualvpn-tuncheck              # TUN: adapter + routes (as admin)
```
