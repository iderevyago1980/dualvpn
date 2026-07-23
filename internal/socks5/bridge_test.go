package socks5

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
)

// freePort возвращает свободный локальный TCP-порт.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("поиск свободного порта: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestNewBridgeValidation(t *testing.T) {
	in := make(chan []byte)
	out := make(chan []byte)

	if _, err := NewBridge(-1, in, out); err == nil {
		t.Error("отрицательный порт должен давать ошибку")
	}
	if _, err := NewBridge(70000, in, out); err == nil {
		t.Error("порт > 65535 должен давать ошибку")
	}
	if _, err := NewBridge(1080, nil, out); err == nil {
		t.Error("nil-канал ingress должен давать ошибку")
	}
	if _, err := NewBridge(1080, in, nil); err == nil {
		t.Error("nil-канал egress должен давать ошибку")
	}

	b, err := NewBridge(freePort(t), in, out)
	if err != nil {
		t.Fatalf("корректные параметры: %v", err)
	}
	defer b.Close()

	if err := b.SetLocalAddress("не-адрес"); err == nil {
		t.Error("некорректный IP должен давать ошибку")
	}
	if err := b.SetLocalAddress("fe80::1"); err == nil {
		t.Error("IPv6-адрес должен давать ошибку (ожидается IPv4)")
	}
	if err := b.SetLocalAddress("10.0.0.2"); err != nil {
		t.Errorf("корректный IPv4: %v", err)
	}
}

func TestBridgeStartClose(t *testing.T) {
	in := make(chan []byte, 8)
	out := make(chan []byte, 8)
	port := freePort(t)

	b, err := NewBridge(port, in, out)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	if err := b.SetLocalAddress("10.0.0.2"); err != nil {
		t.Fatalf("SetLocalAddress: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := b.Start(ctx); err == nil {
		t.Error("повторный Start должен давать ошибку")
	}

	// SOCKS5-сервер слушает: проверяем greeting (метод "без аутентификации")
	conn, err := net.DialTimeout("tcp", b.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("подключение к SOCKS5: %v", err)
	}
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("ответ на greeting: %v", err)
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		t.Errorf("ожидался ответ 05 00, получено % x", reply)
	}
	conn.Close()

	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("повторный Close (идемпотентность): %v", err)
	}

	// После Close listener закрыт
	if _, err := net.DialTimeout("tcp", b.Addr(), 500*time.Millisecond); err == nil {
		t.Error("после Close порт не должен приниматься соединения")
	}
}

// socksConnect выполняет SOCKS5-handshake и команду CONNECT к ip:port.
func socksConnect(t *testing.T, proxyAddr string, ip net.IP, port uint16) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 3*time.Second)
	if err != nil {
		t.Fatalf("подключение к SOCKS5: %v", err)
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("ответ на greeting: %v", err)
	}

	req := []byte{0x05, 0x01, 0x00, 0x01} // CONNECT, IPv4
	req = append(req, ip.To4()...)
	req = binary.BigEndian.AppendUint16(req, port)
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("CONNECT: %v", err)
	}
	resp := make([]byte, 10) // VER REP RSV ATYP BND.ADDR(4) BND.PORT(2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("ответ на CONNECT: %v", err)
	}
	if resp[1] != 0x00 {
		t.Fatalf("CONNECT отклонён, код %d", resp[1])
	}
	return conn
}

// TestBridgeEndToEnd соединяет два моста «спина к спине» (egress одного —
// ingress другого), поднимает echo-сервер внутри netstack второго моста
// и проверяет, что SOCKS5-клиент первого моста достучался до него
// и получил свои данные обратно. Это прогоняет весь путь:
// SOCKS5 → dialVPN → netstack1 → каналы (туннель) → netstack2 → сервер.
func TestBridgeEndToEnd(t *testing.T) {
	aToB := make(chan []byte, 64)
	bToA := make(chan []byte, 64)

	b1, err := NewBridge(freePort(t), bToA, aToB)
	if err != nil {
		t.Fatalf("NewBridge b1: %v", err)
	}
	defer b1.Close()
	if err := b1.SetLocalAddress("10.0.0.1"); err != nil {
		t.Fatalf("SetLocalAddress b1: %v", err)
	}

	b2, err := NewBridge(freePort(t), aToB, bToA)
	if err != nil {
		t.Fatalf("NewBridge b2: %v", err)
	}
	defer b2.Close()
	if err := b2.SetLocalAddress("10.0.0.2"); err != nil {
		t.Fatalf("SetLocalAddress b2: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b1.Start(ctx); err != nil {
		t.Fatalf("Start b1: %v", err)
	}
	if err := b2.Start(ctx); err != nil {
		t.Fatalf("Start b2: %v", err)
	}

	// Echo-сервер внутри netstack второго моста — играет роль хоста VPN-сети
	const echoPort = 7777
	ln, err := gonet.ListenTCP(b2.stack, tcpip.FullAddress{
		NIC:  nicID,
		Addr: tcpip.AddrFromSlice(net.ParseIP("10.0.0.2").To4()),
		Port: echoPort,
	}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("ListenTCP в netstack: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c) //nolint:errcheck // echo до EOF
			}(c)
		}
	}()

	conn := socksConnect(t, b1.Addr(), net.ParseIP("10.0.0.2"), echoPort)
	defer conn.Close()

	msg := []byte("ping через два netstack")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("отправка: %v", err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("приём эха: %v", err)
	}
	if string(got) != string(msg) {
		t.Errorf("эхо не совпало: %q != %q", got, msg)
	}
}

// TestBridgeIgnoresGarbage проверяет, что мусорные и пустые пакеты
// в ingress не роняют мост.
func TestBridgeIgnoresGarbage(t *testing.T) {
	in := make(chan []byte, 8)
	out := make(chan []byte, 8)

	b, err := NewBridge(freePort(t), in, out)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	defer b.Close()
	if err := b.SetLocalAddress("10.0.0.3"); err != nil {
		t.Fatalf("SetLocalAddress: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	in <- nil
	in <- []byte{}
	in <- []byte{0xde, 0xad, 0xbe, 0xef} // версия IP = 13 — не IPv4/IPv6
	in <- []byte(fmt.Sprintf("%1024d", 0))

	// Мост жив: SOCKS5 по-прежнему отвечает
	time.Sleep(100 * time.Millisecond)
	conn, err := net.DialTimeout("tcp", b.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("SOCKS5 недоступен после мусорных пакетов: %v", err)
	}
	conn.Close()
}
