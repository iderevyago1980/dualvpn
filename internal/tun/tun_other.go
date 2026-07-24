//go:build !linux && !windows

package tun

import (
	"fmt"
	"runtime"
)

// Create — TUN-режим на этой платформе не поддерживается.
// Реализованы только Linux (/dev/net/tun) и Windows (wintun);
// на остальных платформах используйте режим SOCKS5.
func Create(cfg Config) (*Device, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("TUN-режим не поддерживается на %s — используйте SOCKS5", runtime.GOOS)
}
