// Форк sslcon/session: per-tunnel контейнер сессии вместо глобала session.Sess.
package sslcon

import (
	sslsession "sslcon/session"
)

// ConnSession — сессия установленного соединения (TLS/DTLS-каналы, параметры
// маршрутизации от сервера). Сам тип не форкаем — используем из sslcon/session.
type ConnSession = sslsession.ConnSession

// Session — состояние сессии одного туннеля. Форк session.Session:
// глобал session.Sess заменён на экземпляр, принадлежащий туннелю.
type Session struct {
	CSess       *ConnSession
	ActiveClose bool
}

// NewSession создаёт пустую сессию туннеля.
func NewSession() *Session {
	return &Session{}
}

// Close помечает закрытие как инициированное клиентом и закрывает ConnSession.
//
// ВНИМАНИЕ: ConnSession.Close() из sslcon/session всё ещё обращается к
// глобалу session.Sess (обнуляет Sess.CSess и закрывает Sess.CloseChan) —
// это будет устранено при форке фазы dial/CSTP. До тех пор CSess должен
// создаваться через session.Sess.NewConnSession, иначе Close паникует.
func (s *Session) Close() {
	s.ActiveClose = true
	if s.CSess != nil {
		s.CSess.Close()
		s.CSess = nil
	}
}
