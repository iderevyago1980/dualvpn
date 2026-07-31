package socks5

import (
	"context"
	"net"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// TestMatchesSplitDomain — попадание имени в зону split-DNS.
func TestMatchesSplitDomain(t *testing.T) {
	r := newTunnelResolver(DNSConfig{Domains: []string{"corp.example", ".intranet.example", "0.10.in-addr.arpa"}}, nil)

	in := []string{"corp.example", "CORP.EXAMPLE", "host.corp.example", "wiki.intranet.example", "1.0.10.in-addr.arpa", "host.corp.example."}
	for _, name := range in {
		if !r.matchesSplitDomain(name) {
			t.Errorf("%q должно попадать в split-DNS", name)
		}
	}
	out := []string{"example.com", "notcorp.example", "corp.example.evil.com", ""}
	for _, name := range out {
		if r.matchesSplitDomain(name) {
			t.Errorf("%q не должно попадать в split-DNS", name)
		}
	}
}

// TestUseTunnel — выбор резолвера: внутренний DNS или системный.
func TestUseTunnel(t *testing.T) {
	// Без DNS-серверов туннеля спрашивать некого.
	r := newTunnelResolver(DNSConfig{Domains: []string{"corp.example"}}, nil)
	if r.useTunnel("host.corp.example") {
		t.Error("без серверов DNS туннель использоваться не должен")
	}

	// Есть split-DNS: через туннель идут только его зоны.
	r = newTunnelResolver(DNSConfig{Servers: []string{"10.0.0.11"}, Domains: []string{"corp.example"}}, nil)
	if !r.useTunnel("host.corp.example") {
		t.Error("имя из split-DNS должно разрешаться через туннель")
	}
	if r.useTunnel("example.com") {
		t.Error("внешнее имя не должно идти во внутренний DNS при заданном split-DNS")
	}

	// tunnel-all-dns: через туннель идёт всё.
	r = newTunnelResolver(DNSConfig{Servers: []string{"10.0.0.11"}, Domains: []string{"corp.example"}, TunnelAll: true}, nil)
	if !r.useTunnel("example.com") {
		t.Error("при tunnel-all-dns через туннель должны идти все имена")
	}

	// Сервер не прислал split-DNS: пробуем туннель для всех имён.
	r = newTunnelResolver(DNSConfig{Servers: []string{"10.0.0.11"}}, nil)
	if !r.useTunnel("example.com") {
		t.Error("без списка split-DNS имена должны сначала спрашиваться у DNS туннеля")
	}
}

// TestResolveViaTunnel — полный путь: запрос уходит через dial внутрь
// туннеля, ответ разбирается и превращается в IP.
func TestResolveViaTunnel(t *testing.T) {
	want := net.IPv4(10, 6, 1, 25).To4()
	srv, addr := startFakeDNS(t, want)
	defer srv.Close()

	// dial подменяет netstack: соединение уходит на локальный фейковый DNS.
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return net.Dial(network, addr)
	}
	r := newTunnelResolver(DNSConfig{Servers: []string{"10.0.0.11"}, Domains: []string{"corp.example"}}, dial)

	_, got, err := r.Resolve(context.Background(), "wiki.corp.example")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("Resolve вернул %v, ожидалось %v", got, want)
	}
}

// TestResolveSplitDomainDoesNotFallBack — имя корпоративной зоны не должно
// уходить в системный (публичный) резолвер: это и утечка имени, и заведомо
// неверный ответ.
func TestResolveSplitDomainDoesNotFallBack(t *testing.T) {
	dial := func(context.Context, string, string) (net.Conn, error) {
		return nil, net.ErrClosed
	}
	r := newTunnelResolver(DNSConfig{Servers: []string{"10.0.0.11"}, Domains: []string{"corp.example"}}, dial)
	r.system = failingResolver{t}

	if _, _, err := r.Resolve(context.Background(), "wiki.corp.example"); err == nil {
		t.Error("ожидалась ошибка, а не обращение к системному резолверу")
	}
}

// failingResolver проваливает тест при любом обращении.
type failingResolver struct{ t *testing.T }

func (f failingResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
	f.t.Error("системный резолвер не должен вызываться для имён split-DNS")
	return nil, nil
}

// startFakeDNS поднимает UDP-сервер, отвечающий одной A-записью.
func startFakeDNS(t *testing.T, answer net.IP) (net.PacketConn, string) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var msg dnsmessage.Message
			if err := msg.Unpack(buf[:n]); err != nil || len(msg.Questions) == 0 {
				continue
			}
			var a [4]byte
			copy(a[:], answer.To4())
			resp := dnsmessage.Message{
				Header:    dnsmessage.Header{ID: msg.Header.ID, Response: true},
				Questions: msg.Questions,
				Answers: []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{
						Name:  msg.Questions[0].Name,
						Type:  dnsmessage.TypeA,
						Class: dnsmessage.ClassINET,
						TTL:   60,
					},
					Body: &dnsmessage.AResource{A: a},
				}},
			}
			out, err := resp.Pack()
			if err != nil {
				continue
			}
			_, _ = pc.WriteTo(out, from)
		}
	}()
	return pc, pc.LocalAddr().String()
}
