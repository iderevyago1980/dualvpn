// Spike 003: sslcon vs Cisco ASA — проверка совместимости.
// Тестируем только InitAuth (TLS handshake + XML-форма) — не нужен пароль.
package main

import (
	"fmt"
	"time"

	"sslcon/auth"
	"sslcon/base"
)

func main() {
	hosts := []string{"vpn2.astralinux.ru", "vpn.mt-integration.ru"}

	for _, host := range hosts {
		fmt.Printf("=== %s ===\n", host)

		// Сброс глобального состояния
		auth.Prof = &auth.Profile{Initialized: false}
		auth.WebVpnCookie = ""

		base.Cfg.LogLevel = "info"
		base.Cfg.InsecureSkipVerify = true

		auth.Prof.Host = host
		auth.Prof.HostWithPort = host + ":443"
		auth.Prof.Scheme = "https://"
		auth.Prof.Username = "test"
		auth.Prof.Password = "test"
		auth.Prof.Group = "Basic 2FA"

		start := time.Now()
		err := auth.InitAuth()
		if err != nil {
			fmt.Printf("  ❌ InitAuth FAIL: %v (%v)\n\n", err, time.Since(start))
			continue
		}
		fmt.Printf("  ✅ InitAuth OK (%v)\n", time.Since(start))
		fmt.Printf("  AuthPath: %s\n", auth.Prof.AuthPath)
		fmt.Printf("  TunnelGroup: %s\n", auth.Prof.TunnelGroup)
		fmt.Printf("  GroupAlias: %s\n", auth.Prof.GroupAlias)
		fmt.Printf("  ConfigHash: %s\n", auth.Prof.ConfigHash)

		if auth.Conn != nil {
			state := auth.Conn.ConnectionState()
			fmt.Printf("  TLS: %s, Cipher: 0x%04x\n", tlsVersionString(state.Version), state.CipherSuite)
			auth.Conn.Close()
		}
		fmt.Println()
	}
}

func tlsVersionString(v uint16) string {
	switch v {
	case 0x0301:
		return "TLS1.0"
	case 0x0302:
		return "TLS1.1"
	case 0x0303:
		return "TLS1.2"
	case 0x0304:
		return "TLS1.3"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}
