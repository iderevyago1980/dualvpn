// Package checks — переиспользуемые сетевые проверки E2E-стенда:
// HTTP-GET напрямую (TUN) и через SOCKS5-прокси (режим socks5).
package checks

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/proxy"
)

// DirectClient — обычный HTTP-клиент с заданным таймаутом (для TUN-режима,
// где маршрут до внутренней сети уже в таблице маршрутизации ОС).
func DirectClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// SocksClient строит HTTP-клиент, весь трафик которого идёт через SOCKS5-прокси
// proxyAddr (формат "host:port") — точку, поднятую туннелем в режиме socks5.
func SocksClient(proxyAddr string, timeout time.Duration) (*http.Client, error) {
	dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("socks5-диалер %s: %w", proxyAddr, err)
	}
	ctxDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("socks5-диалер не поддерживает контекст")
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return ctxDialer.DialContext(ctx, network, addr)
		},
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

// GetBody выполняет GET и возвращает статус-код и тело ответа целиком.
func GetBody(client *http.Client, url string) (int, string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(body), nil
}
