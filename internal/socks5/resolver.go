// Резолвер имён внутри VPN-туннеля.
//
// В SOCKS5-режиме приложение отдаёт прокси доменное имя, а не адрес. Раньше
// имя разрешалось системным резолвером хоста — то есть публичным DNS, — и
// внутренние имена корпоративной сети не разрешались вовсе: подключение
// «работало», а обратиться к внутреннему ресурсу по имени было нельзя.
//
// Здесь запрос уходит на DNS-серверы, выданные шлюзом (X-CSTP-DNS), причём
// по тому же netstack, что и остальной трафик туннеля, — то есть внутри VPN.
package socks5

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// dnsTimeout — предел ожидания ответа одного DNS-сервера.
const dnsTimeout = 5 * time.Second

// dnsUDPLimit — размер буфера для UDP-ответа (классический предел DNS).
const dnsUDPLimit = 512

// DNSConfig — параметры разрешения имён, полученные от шлюза.
type DNSConfig struct {
	Servers   []string // адреса DNS-серверов внутри VPN (X-CSTP-DNS)
	Domains   []string // суффиксы split-DNS (X-CSTP-Split-DNS)
	TunnelAll bool     // все запросы идут через VPN (X-CSTP-Tunnel-All-DNS)
}

// tunnelResolver реализует gosocks5.NameResolver: имена, относящиеся к VPN,
// разрешает через DNS-серверы шлюза, остальные — системным резолвером.
type tunnelResolver struct {
	cfg    DNSConfig
	dial   DialFunc // соединение внутри netstack (Bridge.dialVPN)
	system interface {
		LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
	}
}

// newTunnelResolver создаёт резолвер поверх dial внутри туннеля.
func newTunnelResolver(cfg DNSConfig, dial DialFunc) *tunnelResolver {
	return &tunnelResolver{cfg: cfg, dial: dial, system: net.DefaultResolver}
}

// Источники ответа — возвращаются диагностикой, чтобы было видно, кто
// именно разрешил имя: DNS внутри VPN или системный резолвер хоста.
const (
	SourceVPN     = "dns-vpn"
	SourceSystem  = "dns-система"
	SourceLiteral = "литерал"
)

// Resolve разрешает имя. Сигнатура задана интерфейсом armon/go-socks5.
func (r *tunnelResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	ip, _, err := r.resolveWithSource(ctx, name)
	return ctx, ip, err
}

// resolveWithSource — то же разрешение имени, но сообщает источник ответа.
func (r *tunnelResolver) resolveWithSource(ctx context.Context, name string) (net.IP, string, error) {
	if ip := net.ParseIP(name); ip != nil {
		return ip, SourceLiteral, nil
	}

	if r.useTunnel(name) {
		ip, err := r.lookupViaTunnel(ctx, name)
		if err == nil {
			return ip, SourceVPN, nil
		}
		// Имя из split-DNS принадлежит корпоративной зоне: спрашивать о нём
		// публичный DNS бессмысленно (и означало бы утечку имени наружу).
		if r.matchesSplitDomain(name) {
			return nil, SourceVPN, fmt.Errorf("socks5: DNS VPN не разрешил %q: %w", name, err)
		}
	}

	ips, err := r.system.LookupIP(ctx, "ip4", name)
	if err != nil {
		return nil, SourceSystem, fmt.Errorf("socks5: не удалось разрешить %q: %w", name, err)
	}
	if len(ips) == 0 {
		return nil, SourceSystem, fmt.Errorf("socks5: имя %q не разрешено", name)
	}
	return ips[0], SourceSystem, nil
}

// useTunnel решает, спрашивать ли DNS внутри VPN. Через туннель идут:
// имена из split-DNS; все имена при tunnel-all-dns; а также все имена,
// когда сервер список split-DNS не прислал (тогда системный резолвер
// остаётся запасным вариантом).
func (r *tunnelResolver) useTunnel(name string) bool {
	if len(r.cfg.Servers) == 0 {
		return false
	}
	return r.cfg.TunnelAll || len(r.cfg.Domains) == 0 || r.matchesSplitDomain(name)
}

// matchesSplitDomain проверяет, попадает ли имя в зону split-DNS.
func (r *tunnelResolver) matchesSplitDomain(name string) bool {
	host := strings.ToLower(strings.TrimSuffix(name, "."))
	for _, d := range r.cfg.Domains {
		d = strings.ToLower(strings.Trim(strings.TrimSpace(d), "."))
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// lookupViaTunnel опрашивает DNS-серверы VPN по очереди до первого ответа.
func (r *tunnelResolver) lookupViaTunnel(ctx context.Context, name string) (net.IP, error) {
	var errs []string
	for _, server := range r.cfg.Servers {
		ip, err := r.queryServer(ctx, strings.TrimSpace(server), name)
		if err == nil {
			return ip, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", server, err))
	}
	if len(errs) == 0 {
		return nil, errors.New("нет DNS-серверов VPN")
	}
	return nil, errors.New(strings.Join(errs, "; "))
}

// queryServer отправляет A-запрос одному серверу: сперва UDP, при усечённом
// ответе — повтор по TCP (там же, внутри туннеля).
func (r *tunnelResolver) queryServer(ctx context.Context, server, name string) (net.IP, error) {
	query, err := buildQuery(name)
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(server, "53")

	ctx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()

	resp, truncated, err := r.exchangeUDP(ctx, addr, query)
	if err != nil {
		return nil, err
	}
	if truncated {
		resp, err = r.exchangeTCP(ctx, addr, query)
		if err != nil {
			return nil, err
		}
	}
	return firstA(resp)
}

// exchangeUDP выполняет обмен по UDP; второе значение — признак усечения.
func (r *tunnelResolver) exchangeUDP(ctx context.Context, addr string, query []byte) ([]byte, bool, error) {
	conn, err := r.dial(ctx, "udp", addr)
	if err != nil {
		return nil, false, err
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := conn.Write(query); err != nil {
		return nil, false, err
	}
	buf := make([]byte, dnsUDPLimit)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, false, err
	}
	msg := buf[:n]

	var parsed dnsmessage.Message
	if err := parsed.Unpack(msg); err != nil {
		return nil, false, fmt.Errorf("разбор ответа DNS: %w", err)
	}
	return msg, parsed.Header.Truncated, nil
}

// exchangeTCP повторяет запрос по TCP (формат с 2-байтовым префиксом длины).
func (r *tunnelResolver) exchangeTCP(ctx context.Context, addr string, query []byte) ([]byte, error) {
	conn, err := r.dial(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	framed := append([]byte{byte(len(query) >> 8), byte(len(query))}, query...)
	if _, err := conn.Write(framed); err != nil {
		return nil, err
	}
	var length [2]byte
	if _, err := io.ReadFull(conn, length[:]); err != nil {
		return nil, err
	}
	resp := make([]byte, int(length[0])<<8|int(length[1]))
	if _, err := io.ReadFull(conn, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// buildQuery собирает DNS-запрос типа A для указанного имени.
func buildQuery(name string) ([]byte, error) {
	fqdn := name
	if !strings.HasSuffix(fqdn, ".") {
		fqdn += "."
	}
	qname, err := dnsmessage.NewName(fqdn)
	if err != nil {
		return nil, fmt.Errorf("некорректное имя %q: %w", name, err)
	}
	msg := dnsmessage.Message{
		// ID не используется: соединение точечное (connected UDP/TCP),
		// на него приходит ровно один ответ.
		Header: dnsmessage.Header{RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name:  qname,
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		}},
	}
	return msg.Pack()
}

// firstA достаёт первый A-адрес из ответа.
func firstA(resp []byte) (net.IP, error) {
	var msg dnsmessage.Message
	if err := msg.Unpack(resp); err != nil {
		return nil, fmt.Errorf("разбор ответа DNS: %w", err)
	}
	if msg.Header.RCode != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("DNS вернул %s", msg.Header.RCode)
	}
	for _, ans := range msg.Answers {
		if a, ok := ans.Body.(*dnsmessage.AResource); ok {
			return net.IP(a.A[:]).To4(), nil
		}
	}
	return nil, errors.New("в ответе DNS нет записи A")
}
