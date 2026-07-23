// Форк sslcon/session: per-tunnel состояние сессии вместо глобала session.Sess.
//
// В оригинале ConnSession.Close() и DtlsSession.Close() обращаются к глобалу
// session.Sess (обнуляют Sess.CSess, закрывают Sess.CloseChan) — при двух
// туннелях закрытие одного ломало бы второй. Здесь ConnSession хранит
// указатель на свою Session, а DtlsSession — на свой ConnSession.
package sslcon

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"go.uber.org/atomic"

	"sslcon/base"
	"sslcon/proto"
	"sslcon/utils"
)

// Session — состояние сессии одного туннеля. Форк session.Session:
// глобал session.Sess заменён на экземпляр, принадлежащий туннелю.
type Session struct {
	SessionToken    string
	PreMasterSecret []byte

	ActiveClose bool
	CloseChan   chan struct{} // закрывается вместе с ConnSession — сигнал наблюдателям (UI)
	CSess       *ConnSession
}

// NewSession создаёт пустую сессию туннеля.
func NewSession() *Session {
	return &Session{}
}

// Close помечает закрытие как инициированное клиентом и закрывает ConnSession.
func (s *Session) Close() {
	s.ActiveClose = true
	if s.CSess != nil {
		s.CSess.Close()
		s.CSess = nil
	}
}

// Stat — счётчики трафика туннеля.
type Stat struct {
	BytesSent     uint64 `json:"bytesSent"`
	BytesReceived uint64 `json:"bytesReceived"`
}

// ConnSession — сессия установленного соединения: параметры, полученные от
// шлюза при CSTP-переговорах, и каналы обмена пакетами между TLS/DTLS-каналами
// и потребителем (TUN-адаптер или SOCKS5-сервер).
// Форк session.ConnSession без динамического split tunneling (в DualVPN
// маршрутизация статическая, через internal/routing).
type ConnSession struct {
	Sess *Session

	ServerAddress string
	LocalAddress  string
	Hostname      string
	TunName       string
	VPNAddress    string // IPv4-адрес клиента внутри туннеля
	VPNMask       string // маска IPv4
	DNS           []string
	MTU           int
	SplitInclude  []string
	SplitExclude  []string

	TLSCipherSuite    string
	TLSDpdTime        int // https://datatracker.ietf.org/doc/html/rfc3706
	TLSKeepaliveTime  int
	DTLSPort          string
	DTLSDpdTime       int
	DTLSKeepaliveTime int
	DTLSId            string // сервер связывает DTLS-канал с CSTP-каналом по этому id
	DTLSCipherSuite   string
	Stat              *Stat

	closeOnce      sync.Once
	CloseChan      chan struct{}
	PayloadIn      chan *proto.Payload // сервер → клиент (без заголовков)
	PayloadOutTLS  chan *proto.Payload // клиент → сервер по TLS
	PayloadOutDTLS chan *proto.Payload // клиент → сервер по DTLS

	DtlsConnected *atomic.Bool
	DtlsSetupChan chan struct{}
	DSess         *DtlsSession

	ResetTLSReadDead  *atomic.Bool
	ResetDTLSReadDead *atomic.Bool
}

// DtlsSession — состояние DTLS-канала одного туннеля.
type DtlsSession struct {
	closeOnce sync.Once
	CloseChan chan struct{}
	cSess     *ConnSession // родительская сессия (в оригинале — глобал Sess.CSess)
}

// NewConnSession разбирает заголовки ответа сервера на CONNECT и создаёт
// сессию соединения. Форк session.Session.NewConnSession.
func (s *Session) NewConnSession(header *http.Header) *ConnSession {
	cSess := &ConnSession{
		Sess:              s,
		Stat:              &Stat{},
		CloseChan:         make(chan struct{}),
		DtlsSetupChan:     make(chan struct{}),
		PayloadIn:         make(chan *proto.Payload, 64),
		PayloadOutTLS:     make(chan *proto.Payload, 64),
		PayloadOutDTLS:    make(chan *proto.Payload, 64),
		DtlsConnected:     atomic.NewBool(false),
		ResetTLSReadDead:  atomic.NewBool(true),
		ResetDTLSReadDead: atomic.NewBool(true),
	}
	cSess.DSess = &DtlsSession{
		CloseChan: make(chan struct{}),
		cSess:     cSess,
	}
	s.CSess = cSess
	s.ActiveClose = false
	s.CloseChan = make(chan struct{})

	cSess.VPNAddress = header.Get("X-CSTP-Address")
	cSess.VPNMask = header.Get("X-CSTP-Netmask")
	cSess.MTU, _ = strconv.Atoi(header.Get("X-CSTP-MTU"))
	cSess.DNS = header.Values("X-CSTP-DNS")
	cSess.SplitInclude = header.Values("X-CSTP-Split-Include")
	cSess.SplitExclude = header.Values("X-CSTP-Split-Exclude")

	cSess.TLSDpdTime, _ = strconv.Atoi(header.Get("X-CSTP-DPD"))
	cSess.TLSKeepaliveTime, _ = strconv.Atoi(header.Get("X-CSTP-Keepalive"))
	// https://datatracker.ietf.org/doc/html/draft-mavrogiannopoulos-openconnect-02#section-2.1.5.1
	cSess.DTLSId = header.Get("X-DTLS-Session-ID")
	if cSess.DTLSId == "" {
		// Совместимость с новыми версиями ocserv
		cSess.DTLSId = header.Get("X-DTLS-App-ID")
	}
	cSess.DTLSPort = header.Get("X-DTLS-Port")
	cSess.DTLSDpdTime, _ = strconv.Atoi(header.Get("X-DTLS-DPD"))
	cSess.DTLSKeepaliveTime, _ = strconv.Atoi(header.Get("X-DTLS-Keepalive"))
	if base.Cfg.NoDTLS {
		cSess.DTLSCipherSuite = "Unknown"
	} else {
		cSess.DTLSCipherSuite = header.Get("X-DTLS12-CipherSuite")
	}

	return cSess
}

// DPDTimer периодически отправляет DPD-запросы (dead peer detection)
// по TLS- и DTLS-каналам. Форк session.ConnSession.DPDTimer.
func (cSess *ConnSession) DPDTimer() {
	go func() {
		defer func() {
			base.Info("dead peer detection timer exit")
		}()
		base.Debug("TLSDpdTime:", cSess.TLSDpdTime, "TLSKeepaliveTime", cSess.TLSKeepaliveTime,
			"DTLSDpdTime", cSess.DTLSDpdTime, "DTLSKeepaliveTime", cSess.DTLSKeepaliveTime)
		// Упрощение: проверка не чаще раза в 10 секунд, минимум 5 секунд запаса
		dpdTime := utils.Min(cSess.TLSDpdTime, cSess.DTLSDpdTime) - 5
		if dpdTime < 10 {
			dpdTime = 10
		}
		ticker := time.NewTicker(time.Duration(dpdTime) * time.Second)

		tlsDpd := proto.Payload{
			Type: 0x03,
			Data: make([]byte, 0, 8),
		}
		dtlsDpd := proto.Payload{
			Type: 0x03,
			Data: make([]byte, 0, 1),
		}

		for {
			select {
			case <-ticker.C:
				select {
				case cSess.PayloadOutTLS <- &tlsDpd:
				default:
				}
				if cSess.DtlsConnected.Load() {
					select {
					case cSess.PayloadOutDTLS <- &dtlsDpd:
					default:
					}
				}
			case <-cSess.CloseChan:
				ticker.Stop()
				return
			}
		}
	}()
}

// ReadDeadTimer раз в 4 секунды разрешает каналам обновить read deadline —
// дешевле, чем сброс на каждой итерации чтения. Форк session.ConnSession.ReadDeadTimer.
func (cSess *ConnSession) ReadDeadTimer() {
	go func() {
		defer func() {
			base.Info("read dead timer exit")
		}()
		ticker := time.NewTicker(4 * time.Second)
		for range ticker.C {
			select {
			case <-cSess.CloseChan:
				ticker.Stop()
				return
			default:
				cSess.ResetTLSReadDead.Store(true)
				cSess.ResetDTLSReadDead.Store(true)
			}
		}
	}()
}

// Close закрывает сессию соединения (идемпотентно). В отличие от оригинала
// работает только со своей Session, не с глобалом.
func (cSess *ConnSession) Close() {
	cSess.closeOnce.Do(func() {
		if cSess.DtlsConnected.Load() {
			cSess.DSess.Close()
		}
		close(cSess.CloseChan)
		cSess.Sess.CSess = nil
		close(cSess.Sess.CloseChan)
	})
}

// Close закрывает DTLS-канал (идемпотентно), TLS-канал продолжает работать.
func (dSess *DtlsSession) Close() {
	dSess.closeOnce.Do(func() {
		close(dSess.CloseChan)
		dSess.cSess.DtlsConnected.Store(false)
		dSess.cSess.DTLSCipherSuite = ""
	})
}
