// Bridge — SOCKS5-сервер поверх gVisor netstack.
//
// Пакеты IP из VPN-туннеля (tunnel.PacketFlow) подаются в userspace-стек
// gVisor как ingress; исходящие пакеты стека уходят обратно в туннель.
// SOCKS5-клиенты подключаются к 127.0.0.1:port, их CONNECT-запросы
// открываются через gonet.DialContextTCP внутри netstack — то есть трафик
// приложений попадает в VPN-сеть без TUN-адаптера и админ-прав.
//
// Поддерживается команда CONNECT (TCP). UDP ASSOCIATE и BIND не
// поддерживаются — их нет и в используемом SOCKS5-фронтенде armon/go-socks5.
package socks5

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

// nicID — идентификатор единственного NIC внутри netstack.
const nicID = tcpip.NICID(1)

// bridgeMTU — MTU виртуального NIC; совпадает с X-CSTP-MTU туннеля.
const bridgeMTU = 1399

// channelSize — ёмкость очереди пакетов channel.Endpoint.
const channelSize = 512

// Bridge — SOCKS5-сервер поверх gVisor netstack.
// Читает IP-пакеты из tunnel, передаёт в gVisor, SOCKS5-клиенты
// подключаются на локальный порт и получают доступ к VPN-сети.
type Bridge struct {
	port    int
	stack   *stack.Stack
	linkEP  *channel.Endpoint
	inChan  <-chan []byte // пакеты из tunnel (ingress)
	outChan chan<- []byte // пакеты в tunnel (egress)
	srv     *Server       // SOCKS5-фронтенд (armon/go-socks5) с dial через netstack

	localAddr tcpip.Address // IPv4-адрес клиента внутри VPN (источник исходящих)

	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	started   bool
}

// NewBridge создаёт мост: gVisor-стек с одним NIC (channel.Endpoint)
// и SOCKS5-сервер на указанном локальном порту. Каналы in/out — packet flow
// туннеля (tunnel.PacketFlow). Стек запускается методом Start.
func NewBridge(port int, in <-chan []byte, out chan<- []byte) (*Bridge, error) {
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("socks5: некорректный порт %d", port)
	}
	if in == nil || out == nil {
		return nil, errors.New("socks5: каналы packet flow не заданы")
	}

	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})

	linkEP := channel.New(channelSize, bridgeMTU, "")
	if tcpipErr := s.CreateNIC(nicID, linkEP); tcpipErr != nil {
		s.Close()
		return nil, fmt.Errorf("socks5: создание NIC: %s", tcpipErr)
	}

	// Все адреса достижимы через единственный NIC (маршрут по умолчанию)
	s.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})

	b := &Bridge{
		port:    port,
		stack:   s,
		linkEP:  linkEP,
		inChan:  in,
		outChan: out,
	}

	srv, err := New(port, b.dialVPN)
	if err != nil {
		s.Close()
		return nil, err
	}
	b.srv = srv
	return b, nil
}

// Port возвращает локальный порт SOCKS5-сервера.
func (b *Bridge) Port() int { return b.port }

// Addr возвращает адрес прослушивания SOCKS5-сервера.
func (b *Bridge) Addr() string { return b.srv.Addr() }

// SetLocalAddress задаёт IPv4-адрес клиента внутри VPN (выданный шлюзом,
// cSess.VPNAddress) — он назначается NIC и используется как источник
// исходящих соединений. Вызывать до Start.
func (b *Bridge) SetLocalAddress(ip string) error {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return fmt.Errorf("socks5: некорректный IPv4-адрес %q", ip)
	}
	b.localAddr = tcpip.AddrFromSlice(parsed.To4())
	return nil
}

// Start назначает адрес NIC, запускает перекачку пакетов между туннелем и
// netstack и начинает принимать SOCKS5-подключения. Не блокирует; работа
// продолжается до Close или отмены ctx.
func (b *Bridge) Start(ctx context.Context) error {
	if b.started {
		return errors.New("socks5: bridge уже запущен")
	}
	if b.localAddr != (tcpip.Address{}) {
		protoAddr := tcpip.ProtocolAddress{
			Protocol:          ipv4.ProtocolNumber,
			AddressWithPrefix: tcpip.AddressWithPrefix{Address: b.localAddr, PrefixLen: 32},
		}
		if tcpipErr := b.stack.AddProtocolAddress(nicID, protoAddr, stack.AddressProperties{}); tcpipErr != nil {
			return fmt.Errorf("socks5: назначение адреса NIC: %s", tcpipErr)
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	b.cancel = cancel

	b.wg.Add(2)
	go b.injectPackets(ctx)
	go b.extractPackets(ctx)

	if err := b.srv.Start(); err != nil {
		cancel()
		return err
	}
	b.started = true

	// Отмена ctx останавливает мост целиком
	go func() {
		<-ctx.Done()
		_ = b.srv.Stop()
	}()
	return nil
}

// injectPackets читает IP-пакеты туннеля из inChan и подаёт их в netstack
// как входящие (ingress).
func (b *Bridge) injectPackets(ctx context.Context) {
	defer b.wg.Done()
	for {
		select {
		case pkt, ok := <-b.inChan:
			if !ok {
				return // туннель закрыт
			}
			if len(pkt) == 0 {
				continue
			}
			var proto tcpip.NetworkProtocolNumber
			switch header.IPVersion(pkt) {
			case header.IPv4Version:
				proto = header.IPv4ProtocolNumber
			case header.IPv6Version:
				proto = header.IPv6ProtocolNumber
			default:
				continue // не IP-пакет — отбрасываем
			}
			pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{
				Payload: buffer.MakeWithData(pkt),
			})
			b.linkEP.InjectInbound(proto, pkb)
			pkb.DecRef()
		case <-ctx.Done():
			return
		}
	}
}

// extractPackets забирает исходящие пакеты netstack из link endpoint
// и отправляет их в туннель (egress).
func (b *Bridge) extractPackets(ctx context.Context) {
	defer b.wg.Done()
	for {
		pkb := b.linkEP.ReadContext(ctx)
		if pkb == nil {
			return // ctx отменён
		}
		buf := pkb.ToBuffer()
		data := buf.Flatten()
		buf.Release()
		pkb.DecRef()

		select {
		case b.outChan <- data:
		case <-ctx.Done():
			return
		}
	}
}

// dialVPN устанавливает исходящее соединение через netstack — его пакеты
// уходят в VPN-туннель. Подставляется в SOCKS5-сервер как DialFunc.
func (b *Bridge) dialVPN(ctx context.Context, network, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("socks5: разбор адреса %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return nil, fmt.Errorf("socks5: некорректный порт в адресе %q", addr)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		// Домен: разрешаем системным резолвером (go-socks5 обычно уже
		// разрешил имя сам, сюда попадает готовый IP)
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("socks5: не удалось разрешить %q: %w", host, err)
		}
		ip = ips[0]
	}

	var proto tcpip.NetworkProtocolNumber
	var tAddr tcpip.Address
	if ip4 := ip.To4(); ip4 != nil {
		proto = ipv4.ProtocolNumber
		tAddr = tcpip.AddrFromSlice(ip4)
	} else {
		proto = ipv6.ProtocolNumber
		tAddr = tcpip.AddrFromSlice(ip.To16())
	}
	full := tcpip.FullAddress{NIC: nicID, Addr: tAddr, Port: uint16(port)}

	switch network {
	case "tcp", "tcp4", "tcp6":
		return gonet.DialContextTCP(ctx, b.stack, full, proto)
	case "udp", "udp4", "udp6":
		return gonet.DialUDP(b.stack, nil, &full, proto)
	default:
		return nil, fmt.Errorf("socks5: неподдерживаемая сеть %q", network)
	}
}

// Close останавливает SOCKS5-сервер, перекачку пакетов и разрушает netstack.
// Идемпотентен.
func (b *Bridge) Close() error {
	b.closeOnce.Do(func() {
		if b.cancel != nil {
			b.cancel()
		}
		_ = b.srv.Stop()
		b.wg.Wait()
		b.stack.Close()
		b.linkEP.Close()
	})
	return nil
}
