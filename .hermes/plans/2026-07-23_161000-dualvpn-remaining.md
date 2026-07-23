# DualVPN: Remaining Implementation Plan

> **For Hermes:** Execute task-by-task using Claude Code CLI (`claude -p`) with TDD discipline.

**Goal:** Завершить DualVPN — TUN-режим, UI (Wails), тесты, кросс-компиляция Windows.

**Architecture:** Go backend + Wails v3 UI (sidebar, dark theme). Two modes: SOCKS5 (ocproxy, no admin) and TUN (wintun/vpnc-script, admin). OpenConnect as subprocess with 2FA support.

**Tech Stack:** Go 1.24, Wails v3, openconnect, ocproxy, wintun.dll, BurntSushi/toml

---

## Completed (Steps 1-2)

- ✅ `internal/config/config.go` — TOML config, validate, save/load
- ✅ `internal/vpn/openconnect.go` — subprocess wrapper, 2FA, ocproxy, events
- ✅ `internal/mode/detect.go` — admin detection (Linux: geteuid, Windows: PHYSICALDRIVE0)
- ✅ `internal/socks5/server.go` — SOCKS5 server (reserve for gVisor)
- ✅ `main.go` — CLI with -config, -connect, -mode flags
- ✅ `config.example.toml` — 2 endpoints
- ✅ `frontend/index.html` — UI mockup (sidebar, dark theme)

## Cost so far: $3.41 (2 Claude Code passes)

---

### Task 1: TUN-режим — routing manager (TDD)

**Objective:** Создать internal/routing/routing.go для управления route table (split-tunneling)

**Files:**
- Create: `internal/routing/routing.go`
- Create: `internal/routing/routing_test.go`

**Step 1: Write failing test**

```go
// internal/routing/routing_test.go
package routing

import "testing"

func TestParseCIDR(t *testing.T) {
    tests := []struct {
        cidr    string
        network string
        mask    string
        wantErr bool
    }{
        {"192.168.1.0/24", "192.168.1.0", "255.255.255.0", false},
        {"10.0.0.0/8", "10.0.0.0", "255.0.0.0", false},
        {"invalid", "", "", true},
        {"192.168.1.0/33", "", "", true},
    }
    for _, tt := range tests {
        n, m, err := ParseCIDR(tt.cidr)
        if (err != nil) != tt.wantErr {
            t.Errorf("ParseCIDR(%q) err=%v wantErr=%v", tt.cidr, err, tt.wantErr)
        }
        if !tt.wantErr && n != tt.network {
            t.Errorf("ParseCIDR(%q) network=%q want %q", tt.cidr, n, tt.network)
        }
        if !tt.wantErr && m != tt.mask {
            t.Errorf("ParseCIDR(%q) mask=%q want %q", tt.cidr, m, tt.mask)
        }
    }
}

func TestBuildRouteCommand(t *testing.T) {
    // Linux: route add -net 192.168.1.0 netmask 255.255.255.0 gw <gw> dev <iface>
    cmd := BuildAddRouteCommand("linux", "192.168.1.0/24", "10.8.0.1", "tun0")
    if cmd[0] != "route" || cmd[1] != "add" {
        t.Errorf("expected route add, got %v", cmd)
    }
    // Windows: route add 192.168.1.0 mask 255.255.255.0 10.8.0.1 IF <idx>
    cmd = BuildAddRouteCommand("windows", "192.168.1.0/24", "10.8.0.1", "42")
    if cmd[0] != "route" || cmd[1] != "add" {
        t.Errorf("expected route add, got %v", cmd)
    }
}
```

**Step 2:** Run test → FAIL (functions don't exist)

**Step 3:** Implement `ParseCIDR`, `BuildAddRouteCommand`, `BuildDeleteRouteCommand`

**Step 4:** Run test → PASS

**Step 5:** Commit: `feat: routing manager with CIDR parsing and route commands`

---

### Task 2: TUN-режим — tun device manager (TDD)

**Objective:** Создать internal/tun/tun.go — создание/удаление TUN-адаптеров

**Files:**
- Create: `internal/tun/tun.go`
- Create: `internal/tun/tun_test.go`

**Step 1: Write failing test**

```go
// internal/tun/tun_test.go
package tun

import "testing"

func TestTunConfigValidate(t *testing.T) {
    cfg := Config{Name: "vpn1", Address: "10.8.0.2", MTU: 1400}
    if err := cfg.Validate(); err != nil {
        t.Errorf("valid config: %v", err)
    }
    invalid := Config{Name: "", Address: "10.8.0.2", MTU: 1400}
    if err := invalid.Validate(); err == nil {
        t.Error("empty name should fail validation")
    }
}
```

**Step 3:** Implement Config struct, Validate(), platform-specific create/destroy

**Step 5:** Commit: `feat: TUN device manager with validation`

---

### Task 3: VPN tunnel manager — multi-tunnel orchestration (TDD)

**Objective:** Создать internal/vpn/manager.go — управляет 2+ туннелями одновременно

**Files:**
- Create: `internal/vpn/manager.go`
- Create: `internal/vpn/manager_test.go`

**Test:** Manager starts 2 tunnels, routes events per-tunnel, stops all cleanly

**Commit:** `feat: multi-tunnel manager with event routing`

---

### Task 4: Wails v3 — backend bindings

**Objective:** Создать Wails-биндинги — методы для UI: GetTunnels, ConnectTunnel, DisconnectTunnel, Submit2FA, GetStatus, GetLogs, SwitchMode, SaveConfig

**Files:**
- Create: `internal/ui/app.go` — Wails App struct with all methods
- Modify: `main.go` — Wails startup instead of CLI

**Commit:** `feat: Wails backend bindings for UI`

---

### Task 5: Wails v3 — frontend integration

**Objective:** Адаптировать frontend/index.html mockup для Wails — привязать кнопки к Go-методам через wails runtime

**Files:**
- Modify: `frontend/index.html` — add wails.js runtime calls
- Create: `frontend/app.js` — wails event handlers
- Create: `frontend/style.css` — extracted CSS

**Commit:** `feat: Wails frontend with live backend bindings`

---

### Task 6: System tray

**Objective:** Системный трей — иконка, меню (Подключить/Отключить/Выход), статусы

**Files:**
- Modify: `internal/ui/app.go` — tray integration
- Create: `internal/ui/tray.go` — tray management

**Commit:** `feat: system tray with status indicators`

---

### Task 7: Cross-compilation for Windows

**Objective:** Кросс-компиляция .exe для Windows с wintun.dll в комплекте

**Files:**
- Create: `build/windows/build.bat` — cross-compile script
- Download: `wintun.dll` (from wintun.net)
- Create: `Makefile` — build targets (linux, windows, darwin)

**Commit:** `feat: Windows cross-compilation with wintun.dll`

---

### Task 8: Integration tests with real servers

**Objective:** Тест с реальными серверами vpn1.example.com и vpn2.example.com

**Manual step** — requires VPN credentials and 2FA codes from user.

---

## Execution order

Tasks 1-3: Claude Code CLI (Go backend, TDD)
Tasks 4-6: Claude Code CLI (Wails UI)
Task 7: Claude Code CLI (build)
Task 8: Manual (user provides credentials)
