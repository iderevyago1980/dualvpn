// Package main (dualvpn-harness) — headless-драйвер стенда: поднимает
// туннели через боевой vpn.Manager без Wails.
package main

import (
	"dualvpn/internal/config"
	"dualvpn/internal/vpn"
	"dualvpn/internal/vpn/sslcon"
)

// buildConfigs зеркалит ui.App.registerTunnels, но режим и insecure задаются
// стендом (не автодетекцией). ID туннеля = имя из конфига.
func buildConfigs(cfg *config.Config, mode string, insecure bool) []vpn.TunnelConfig {
	cfgs := make([]vpn.TunnelConfig, 0, len(cfg.Tunnels))
	for _, t := range cfg.Tunnels {
		cfgs = append(cfgs, vpn.TunnelConfig{
			ID: t.Name,
			Opts: sslcon.ClientConfig{
				Host:               t.Endpoint,
				Group:              t.Group,
				Username:           t.Username,
				Password:           t.Password,
				TunName:            t.TunName,
				InsecureSkipVerify: insecure,
			},
			Routes:    t.Routes,
			Mode:      mode,
			SocksPort: t.SocksPort,
		})
	}
	return cfgs
}
