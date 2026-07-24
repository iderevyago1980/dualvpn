package checks

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	socks5 "github.com/armon/go-socks5"
)

// TestGetBodyDirect — прямой GET возвращает статус и тело.
func TestGetBodyDirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("hello-" + r.RemoteAddr))
	}))
	defer srv.Close()

	status, body, err := GetBody(DirectClient(5*time.Second), srv.URL)
	if err != nil {
		t.Fatalf("GetBody: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, ожидалось 200", status)
	}
	if len(body) == 0 || body[:6] != "hello-" {
		t.Fatalf("body = %q, ожидался префикс hello-", body)
	}
}

// TestGetBodyViaSocks — GET через реальный SOCKS5-прокси доходит до цели.
func TestGetBodyViaSocks(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("via-socks"))
	}))
	defer target.Close()

	// Поднимаем настоящий SOCKS5-сервер на свободном порту.
	sconf := &socks5.Config{}
	sserv, err := socks5.New(sconf)
	if err != nil {
		t.Fatalf("socks5.New: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() { _ = sserv.Serve(ln) }()

	client, err := SocksClient(ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("SocksClient: %v", err)
	}
	status, body, err := GetBody(client, target.URL)
	if err != nil {
		t.Fatalf("GetBody via socks: %v", err)
	}
	if status != 200 || body != "via-socks" {
		t.Fatalf("status=%d body=%q", status, body)
	}
}
