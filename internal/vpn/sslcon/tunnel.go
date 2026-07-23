// Форк sslcon/vpn (tunnel.go, tls.go, dtls.go, tun.go, buffer.go):
// установка CSTP-туннеля и packet flow с состоянием на каждый туннель.
//
// Отличия от оригинала:
//   - глобалы reqHeaders, session.Sess, tun.NativeTunDevice заменены на
//     поля структуры Tunnel — два туннеля работают независимо;
//   - TUN-адаптер создаётся через наш internal/tun (Linux: /dev/net/tun,
//     Windows: wintun), настройка адреса и маршрутов — забота вызывающего
//     (internal/routing), а не vpnc;
//   - для SOCKS5-режима вместо TUN-адаптера packet flow выведен в каналы
//     (PacketFlow) либо в явные ReadPacket/WritePacket.
package sslcon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/dtls/v2"

	"sslcon/base"
	"sslcon/proto"
	"sslcon/utils"

	tundev "dualvpn/internal/tun"
)

// Режимы работы туннеля.
const (
	ModeTUN    = "tun"    // трафик через TUN-адаптер (нужны админ-права)
	ModeSOCKS5 = "socks5" // трафик через каналы PacketFlow для SOCKS5-сервера
)

// BufferSize — размер буфера одного пакета (MTU 1399 + заголовки, с запасом).
const BufferSize = 2048

// cstpHeaderLen — длина заголовка STF-фрейма CSTP (TLS-канал).
const cstpHeaderLen = 8

// pool переиспользует буферы пакетов; каналы PayloadIn/PayloadOut*
// передают только указатели.
var pool = sync.Pool{
	New: func() interface{} {
		return &proto.Payload{
			Type: 0x00,
			Data: make([]byte, BufferSize),
		}
	},
}

func getPayloadBuffer() *proto.Payload {
	return pool.Get().(*proto.Payload)
}

func putPayloadBuffer(pl *proto.Payload) {
	// Чужие буферы (DPD-REQ, KEEPALIVE и т.п.) в пул не возвращаем
	if cap(pl.Data) != BufferSize {
		return
	}
	pl.Type = 0x00
	pl.Data = pl.Data[:BufferSize]
	pool.Put(pl)
}

// Route — маршрут, полученный от шлюза (X-CSTP-Split-Include).
type Route struct {
	Network string // сеть, например 10.0.0.0
	Mask    string // маска, например 255.0.0.0
}

// Tunnel — установленный VPN-туннель одного подключения.
type Tunnel struct {
	client  *Client
	session *Session
	cSess   *ConnSession
	mode    string

	tlsConn *tls.Conn
	tunDev  *tundev.Device
	routes  []Route

	mu       sync.Mutex // защищает dtlsConn
	dtlsConn *dtls.Conn

	closeOnce sync.Once

	flowOnce sync.Once
	flowIn   chan []byte
	flowOut  chan []byte
}

// SetupTunnel устанавливает CSTP-туннель поверх аутентифицированного
// TLS-соединения: отправляет HTTP CONNECT, разбирает конфигурацию от сервера
// и запускает TLS- (и при возможности DTLS-) каналы обмена пакетами.
// mode — ModeTUN или ModeSOCKS5. Форк vpn.SetupTunnel + vpn.initTunnel.
func (c *Client) SetupTunnel(mode string) (*Tunnel, error) {
	if mode != ModeTUN && mode != ModeSOCKS5 {
		return nil, fmt.Errorf("sslcon: неизвестный режим туннеля %q (ожидается %q или %q)", mode, ModeTUN, ModeSOCKS5)
	}
	if c.Conn == nil || c.BufR == nil {
		return nil, errors.New("sslcon: нет TLS-соединения — сначала InitAuth и PasswordAuth")
	}
	cookie := c.Cookie()
	if cookie == "" {
		return nil, errors.New("sslcon: нет session token — сначала PasswordAuth")
	}

	sess := NewSession()
	sess.SessionToken = cookie
	// Pre-master secret для legacy-переговоров DTLS
	// https://datatracker.ietf.org/doc/html/draft-mavrogiannopoulos-openconnect-02#section-2.1.5.1
	sess.PreMasterSecret, _ = utils.MakeMasterSecret()

	// Локальный адрес берём из собственного TLS-соединения — у каждого
	// туннеля он свой (в оригинале — глобал base.LocalInterface.Ip4)
	localIP, _, _ := net.SplitHostPort(c.Conn.LocalAddr().String())

	// Бывший глобал reqHeaders — теперь собирается на каждый вызов
	reqHeaders := map[string]string{
		"X-CSTP-VPNAddress-Type": "IPv4",
		// Payload + 8 + заголовок TCP/UDP + IP должно быть меньше 1500 —
		// значение как у AnyConnect
		"X-CSTP-MTU":      "1399",
		"X-CSTP-Base-MTU": "1399",
		// https://datatracker.ietf.org/doc/html/draft-mavrogiannopoulos-openconnect-03#section-2.1.3
		"Cookie":                      "webvpn=" + sess.SessionToken,
		"X-CSTP-Local-VPNAddress-IP4": localIP,
		"X-DTLS-Master-Secret":        hex.EncodeToString(sess.PreMasterSecret),
		// https://gitlab.com/openconnect/ocserv/-/blob/master/src/worker-http.c#L150
		"X-DTLS12-CipherSuite": "ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:AES128-GCM-SHA256",
	}

	req, _ := http.NewRequest("CONNECT", c.Prof.Scheme+c.Prof.HostWithPort+"/CSCOSSLC/tunnel", nil)
	utils.SetCommonHeader(req)
	for k, v := range reqHeaders {
		// req.Header.Set канонизировал бы регистр — сервер ждёт точные имена
		req.Header[k] = []string{v}
	}

	err := req.Write(c.Conn)
	if err != nil {
		c.Conn.Close()
		return nil, err
	}
	// resp.Body закрывается при выходе tlsChannel
	resp, err := http.ReadResponse(c.BufR, req)
	if err != nil {
		c.Conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		c.Conn.Close()
		return nil, fmt.Errorf("tunnel negotiation failed %s", resp.Status)
	}

	if base.Cfg.LogLevel == "Debug" {
		buf := new(bytes.Buffer)
		_ = resp.Header.Write(buf)
		base.Debug(buf.String())
	}

	// Переговоры успешны — разбираем конфигурацию от сервера
	// https://datatracker.ietf.org/doc/html/draft-mavrogiannopoulos-openconnect-03#section-2.1.3
	cSess := sess.NewConnSession(&resp.Header)
	cSess.ServerAddress, _, _ = net.SplitHostPort(c.Conn.RemoteAddr().String())
	cSess.LocalAddress = localIP
	cSess.Hostname = c.Prof.Host
	cSess.TLSCipherSuite = tls.CipherSuiteName(c.Conn.ConnectionState().CipherSuite)

	t := &Tunnel{
		client:  c,
		session: sess,
		cSess:   cSess,
		mode:    mode,
		tlsConn: c.Conn,
		routes:  parseRoutes(cSess),
	}

	base.Info("tls channel negotiation succeeded")

	// https://datatracker.ietf.org/doc/html/draft-mavrogiannopoulos-openconnect-03#section-2.1.4
	go t.tlsChannel(c.Conn, c.BufR, cSess, resp)

	if !base.Cfg.NoDTLS && cSess.DTLSPort != "" {
		// https://datatracker.ietf.org/doc/html/draft-mavrogiannopoulos-openconnect-03#section-2.1.5
		go t.dtlsChannel(cSess)
	}

	cSess.DPDTimer()
	cSess.ReadDeadTimer()

	return t, nil
}

// parseRoutes переводит X-CSTP-Split-Include ("сеть/маска") в []Route.
func parseRoutes(cSess *ConnSession) []Route {
	var routes []Route
	for _, inc := range cSess.SplitInclude {
		network, mask, ok := strings.Cut(inc, "/")
		if ok && network != "" {
			routes = append(routes, Route{Network: network, Mask: mask})
		}
	}
	return routes
}

// Mode возвращает режим туннеля (ModeTUN или ModeSOCKS5).
func (t *Tunnel) Mode() string { return t.mode }

// Routes возвращает маршруты, полученные от шлюза.
func (t *Tunnel) Routes() []Route { return t.routes }

// CSess возвращает сессию соединения (адрес, MTU, DNS и т.д.).
func (t *Tunnel) CSess() *ConnSession { return t.cSess }

// Done возвращает канал, закрываемый при завершении туннеля.
func (t *Tunnel) Done() <-chan struct{} { return t.cSess.CloseChan }

// ReadPacket читает один IP-пакет, пришедший из туннеля (блокирует).
// Возвращает ошибку после закрытия туннеля.
func (t *Tunnel) ReadPacket() ([]byte, error) {
	select {
	case pl := <-t.cSess.PayloadIn:
		// Копируем: буфер возвращается в пул
		packet := make([]byte, len(pl.Data))
		copy(packet, pl.Data)
		putPayloadBuffer(pl)
		return packet, nil
	case <-t.cSess.CloseChan:
		return nil, errors.New("sslcon: туннель закрыт")
	}
}

// WritePacket отправляет один IP-пакет в туннель (DTLS, если канал
// установлен, иначе TLS).
func (t *Tunnel) WritePacket(packet []byte) error {
	if len(packet) > BufferSize-cstpHeaderLen {
		return fmt.Errorf("sslcon: пакет %d байт больше максимума %d", len(packet), BufferSize-cstpHeaderLen)
	}
	pl := getPayloadBuffer()
	n := copy(pl.Data, packet)
	pl.Data = pl.Data[:n]

	if t.cSess.DtlsConnected.Load() {
		select {
		case t.cSess.PayloadOutDTLS <- pl:
		case <-t.cSess.DSess.CloseChan:
			// DTLS упал во время отправки — пакет теряется, дальше пойдёт TLS
			putPayloadBuffer(pl)
		}
		return nil
	}
	select {
	case t.cSess.PayloadOutTLS <- pl:
		return nil
	case <-t.cSess.CloseChan:
		putPayloadBuffer(pl)
		return errors.New("sslcon: туннель закрыт")
	}
}

// PacketFlow возвращает пару каналов для интеграции с SOCKS5-сервером:
// первый — пакеты из туннеля (закрывается при завершении туннеля),
// второй — пакеты в туннель. Повторные вызовы возвращают те же каналы.
func (t *Tunnel) PacketFlow() (<-chan []byte, chan<- []byte) {
	t.flowOnce.Do(func() {
		t.flowIn = make(chan []byte, 64)
		t.flowOut = make(chan []byte, 64)

		// Туннель → потребитель
		go func() {
			defer close(t.flowIn)
			for {
				packet, err := t.ReadPacket()
				if err != nil {
					return
				}
				select {
				case t.flowIn <- packet:
				case <-t.cSess.CloseChan:
					return
				}
			}
		}()

		// Потребитель → туннель
		go func() {
			for {
				select {
				case packet, ok := <-t.flowOut:
					if !ok {
						return
					}
					if err := t.WritePacket(packet); err != nil {
						return
					}
				case <-t.cSess.CloseChan:
					return
				}
			}
		}()
	})
	return t.flowIn, t.flowOut
}

// SetupTUN создаёт TUN-адаптер с именем name и адресом, выданным сервером,
// и запускает перекачку пакетов между адаптером и туннелем. Только для
// ModeTUN; настройка маршрутов — забота вызывающего (internal/routing).
func (t *Tunnel) SetupTUN(name string) error {
	if t.mode != ModeTUN {
		return fmt.Errorf("sslcon: SetupTUN доступен только в режиме %q", ModeTUN)
	}
	dev, err := tundev.Create(tundev.Config{
		Name:    name,
		Address: t.cSess.VPNAddress,
		MTU:     t.cSess.MTU,
	})
	if err != nil {
		return err
	}
	t.tunDev = dev
	t.cSess.TunName = dev.Name

	go t.tunToPayloadOut(dev, t.cSess) // пакеты приложений → туннель
	go t.payloadInToTun(dev, t.cSess)  // туннель → приложения
	return nil
}

// tunToPayloadOut читает IP-пакеты из TUN-адаптера и кладёт их в
// PayloadOutTLS/PayloadOutDTLS. Форк vpn.tunToPayloadOut (offset для darwin
// убран — поддерживаются только Linux и Windows).
func (t *Tunnel) tunToPayloadOut(dev *tundev.Device, cSess *ConnSession) {
	defer func() {
		base.Info("tun to payloadOut exit")
		_ = dev.Close()
	}()

	for {
		pl := getPayloadBuffer()
		n, err := dev.Read(pl.Data)
		if err != nil {
			base.Error("tun to payloadOut error:", err)
			return
		}
		pl.Data = pl.Data[:n]

		if cSess.DtlsConnected.Load() {
			select {
			case cSess.PayloadOutDTLS <- pl:
			case <-cSess.DSess.CloseChan:
			}
		} else {
			select {
			case cSess.PayloadOutTLS <- pl:
			case <-cSess.CloseChan:
				return
			}
		}
	}
}

// payloadInToTun пишет пакеты из PayloadIn в TUN-адаптер. Форк
// vpn.payloadInToTun без vpnc (маршруты снимает internal/routing) и без
// динамического split tunneling.
func (t *Tunnel) payloadInToTun(dev *tundev.Device, cSess *ConnSession) {
	defer func() {
		base.Info("payloadIn to tun exit")
		// Ошибка записи в TUN тоже должна завершить сессию (закрытие
		// идемпотентно — повторный Close безопасен)
		cSess.Close()
		_ = dev.Close()
	}()

	for {
		var pl *proto.Payload
		select {
		case pl = <-cSess.PayloadIn:
		case <-cSess.CloseChan:
			return
		}

		if _, err := dev.Write(pl.Data); err != nil {
			base.Error("payloadIn to tun error:", err)
			return
		}
		putPayloadBuffer(pl)
	}
}

// Close закрывает туннель: сессию, TLS/DTLS-каналы и TUN-адаптер (если был).
// Идемпотентен.
func (t *Tunnel) Close() error {
	t.closeOnce.Do(func() {
		t.session.Close()
		if t.tlsConn != nil {
			_ = t.tlsConn.Close()
		}
		t.mu.Lock()
		if t.dtlsConn != nil {
			_ = t.dtlsConn.Close()
		}
		t.mu.Unlock()
		if t.tunDev != nil {
			_ = t.tunDev.Close()
		}
	})
	return nil
}

// tlsChannel читает STF-фреймы CSTP из TLS-соединения и раскладывает их в
// PayloadIn; параллельно запускает отправку. Форк vpn.tlsChannel.
func (t *Tunnel) tlsChannel(conn *tls.Conn, bufR *bufio.Reader, cSess *ConnSession, resp *http.Response) {
	defer func() {
		base.Info("tls channel exit")
		resp.Body.Close()
		_ = conn.Close()
		cSess.Close()
	}()

	var (
		err           error
		bytesReceived int
		dataLen       uint16
		dead          = time.Duration(cSess.TLSDpdTime+5) * time.Second
	)

	go t.payloadOutTLSToServer(conn, cSess)

	// Чтение данных от сервера: снять заголовок, положить в PayloadIn
	for {
		if cSess.ResetTLSReadDead.Load() {
			_ = conn.SetReadDeadline(time.Now().Add(dead))
			cSess.ResetTLSReadDead.Store(false)
		}

		pl := getPayloadBuffer() // освобождается потребителем PayloadIn
		bytesReceived, err = bufR.Read(pl.Data)
		if err != nil {
			base.Error("tls server to payloadIn error:", err)
			return
		}

		// https://datatracker.ietf.org/doc/html/draft-mavrogiannopoulos-openconnect-03#section-2.2
		switch pl.Data[6] {
		case 0x00: // DATA
			dataLen = binary.BigEndian.Uint16(pl.Data[4:6])
			copy(pl.Data, pl.Data[cstpHeaderLen:cstpHeaderLen+dataLen])
			pl.Data = pl.Data[:dataLen]

			select {
			case cSess.PayloadIn <- pl:
			case <-cSess.CloseChan:
				return
			}
		case 0x04: // DPD-RESP
			base.Debug("tls receive DPD-RESP")
		case 0x03: // DPD-REQ
			pl.Type = 0x04
			select {
			case cSess.PayloadOutTLS <- pl:
			case <-cSess.CloseChan:
				return
			}
		}
		cSess.Stat.BytesReceived += uint64(bytesReceived)
	}
}

// payloadOutTLSToServer добавляет STF-заголовок и отправляет пакеты из
// PayloadOutTLS серверу. Форк vpn.payloadOutTLSToServer.
func (t *Tunnel) payloadOutTLSToServer(conn *tls.Conn, cSess *ConnSession) {
	defer func() {
		base.Info("tls payloadOut to server exit")
		_ = conn.Close()
		cSess.Close()
	}()

	var (
		err       error
		bytesSent int
		pl        *proto.Payload
	)

	for {
		select {
		case pl = <-cSess.PayloadOutTLS:
		case <-cSess.CloseChan:
			return
		}

		if pl.Type == 0x00 {
			l := len(pl.Data)
			// Расширить на длину заголовка, сдвинуть данные, дописать заголовок
			pl.Data = pl.Data[:l+cstpHeaderLen]
			copy(pl.Data[cstpHeaderLen:], pl.Data)
			copy(pl.Data[:cstpHeaderLen], proto.Header)
			binary.BigEndian.PutUint16(pl.Data[4:6], uint16(l))
		} else {
			pl.Data = append(pl.Data[:0], proto.Header...)
			pl.Data[6] = pl.Type
		}
		bytesSent, err = conn.Write(pl.Data)
		if err != nil {
			base.Error("tls payloadOut to server error:", err)
			return
		}
		cSess.Stat.BytesSent += uint64(bytesSent)

		putPayloadBuffer(pl)
	}
}

// dtlsChannel устанавливает DTLS-канал (pion/dtls) и обслуживает его.
// Форк vpn.dtlsChannel: pre-master secret берётся из своей Session,
// а не из глобала session.Sess.
func (t *Tunnel) dtlsChannel(cSess *ConnSession) {
	var (
		conn          *dtls.Conn
		dSess         *DtlsSession
		err           error
		bytesReceived int
		dead          = time.Duration(cSess.DTLSDpdTime+5) * time.Second
	)
	defer func() {
		base.Info("dtls channel exit")
		if conn != nil {
			_ = conn.Close()
		}
		if dSess != nil {
			dSess.Close()
		}
	}()

	port, _ := strconv.Atoi(cSess.DTLSPort)
	addr := &net.UDPAddr{IP: net.ParseIP(cSess.ServerAddress), Port: port}

	id, _ := hex.DecodeString(cSess.DTLSId)

	config := &dtls.Config{
		InsecureSkipVerify:   true,
		ExtendedMasterSecret: dtls.DisableExtendedMasterSecret,
		CipherSuites: func() []dtls.CipherSuiteID {
			switch cSess.DTLSCipherSuite {
			case "ECDHE-ECDSA-AES128-GCM-SHA256":
				return []dtls.CipherSuiteID{dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}
			case "ECDHE-RSA-AES128-GCM-SHA256":
				return []dtls.CipherSuiteID{dtls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}
			case "ECDHE-ECDSA-AES256-GCM-SHA384":
				return []dtls.CipherSuiteID{dtls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384}
			case "ECDHE-RSA-AES256-GCM-SHA384":
				return []dtls.CipherSuiteID{dtls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384}
			default:
				return []dtls.CipherSuiteID{dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}
			}
		}(),
		// Возобновление legacy-сессии по X-DTLS-Session-ID + pre-master secret
		SessionStore: &sessionStore{dtls.Session{ID: id, Secret: t.session.PreMasterSecret}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err = dtls.DialWithContext(ctx, "udp4", addr, config)
	if err != nil {
		base.Error(err)
		close(cSess.DtlsSetupChan) // DTLS-канал не установлен — работаем по TLS
		return
	}

	t.mu.Lock()
	t.dtlsConn = conn
	t.mu.Unlock()

	cSess.DtlsConnected.Store(true)
	dSess = cSess.DSess
	close(cSess.DtlsSetupChan) // DTLS-канал установлен

	cSess.DTLSCipherSuite = dtls.CipherSuiteName(conn.ConnectionState().CipherSuiteID)

	base.Info("dtls channel negotiation succeeded")

	go t.payloadOutDTLSToServer(conn, dSess, cSess)

	// Чтение данных от сервера; без под-горутины, чтобы корректно выйти
	for {
		if cSess.ResetDTLSReadDead.Load() {
			_ = conn.SetReadDeadline(time.Now().Add(dead))
			cSess.ResetDTLSReadDead.Store(false)
		}

		pl := getPayloadBuffer() // освобождается потребителем PayloadIn
		bytesReceived, err = conn.Read(pl.Data)
		if err != nil {
			base.Error("dtls server to payloadIn error:", err)
			return
		}

		// https://datatracker.ietf.org/doc/html/draft-mavrogiannopoulos-openconnect-02#section-2.3
		// Заголовок DTLS-пакета — 1 байт
		switch pl.Data[0] {
		case 0x07: // KEEPALIVE
		case 0x05: // DISCONNECT
			return
		case 0x03: // DPD-REQ
			pl.Type = 0x04
			select {
			case cSess.PayloadOutDTLS <- pl:
			case <-dSess.CloseChan:
			}
		case 0x04: // DPD-RESP
			base.Debug("dtls receive DPD-RESP")
		case 0x00: // DATA
			pl.Data = append(pl.Data[:0], pl.Data[1:bytesReceived]...)
			select {
			case cSess.PayloadIn <- pl:
			case <-dSess.CloseChan:
				return
			}
		}
		cSess.Stat.BytesReceived += uint64(bytesReceived)
	}
}

// payloadOutDTLSToServer добавляет однобайтовый заголовок и отправляет пакеты
// из PayloadOutDTLS серверу. Форк vpn.payloadOutDTLSToServer.
func (t *Tunnel) payloadOutDTLSToServer(conn *dtls.Conn, dSess *DtlsSession, cSess *ConnSession) {
	defer func() {
		base.Info("dtls payloadOut to server exit")
		_ = conn.Close()
		dSess.Close()
	}()

	var (
		err       error
		bytesSent int
		pl        *proto.Payload
	)

	for {
		select {
		case pl = <-cSess.PayloadOutDTLS:
		case <-dSess.CloseChan:
			return
		}

		if pl.Type == 0x00 {
			l := len(pl.Data)
			pl.Data = pl.Data[:l+1]
			copy(pl.Data[1:], pl.Data)
			pl.Data[0] = pl.Type
		} else {
			pl.Data = append(pl.Data[:0], pl.Type)
		}

		bytesSent, err = conn.Write(pl.Data)
		if err != nil {
			base.Error("dtls payloadOut to server error:", err)
			return
		}
		cSess.Stat.BytesSent += uint64(bytesSent)

		putPayloadBuffer(pl)
	}
}

// sessionStore отдаёт pion/dtls заранее согласованную legacy-сессию
// (ID + pre-master secret). Форк vpn.SessionStore.
type sessionStore struct {
	sess dtls.Session
}

func (store *sessionStore) Set([]byte, dtls.Session) error { return nil }

func (store *sessionStore) Get([]byte) (dtls.Session, error) { return store.sess, nil }

func (store *sessionStore) Del([]byte) error { return nil }
