package ui

import (
	"fmt"
	"time"

	"dualvpn/internal/config"
	"dualvpn/internal/ipc"
	"dualvpn/internal/mode"
	"dualvpn/internal/vpn/sslcon"
)

// serviceDialTimeout — сколько ждать ответа службы при подключении к каналу.
const serviceDialTimeout = 3 * time.Second

// tunnelBackend — исполнитель команд подключения. Реализаций две: локальный
// менеджер (режим SOCKS5, а также TUN при запуске от администратора) и
// служба DualVPN (TUN без прав администратора).
type tunnelBackend interface {
	Connect(id string) error
	Disconnect(id string) error
	DisconnectAll()
	Submit2FA(id, code string) error
	Status(id string) (connected bool, mode string)
}

// refreshServiceAvailability проверяет, установлена ли служба, и запоминает
// результат: от него зависит, доступен ли режим TUN без прав администратора.
func (a *App) refreshServiceAvailability() bool {
	available := ipc.Available()
	a.mu.Lock()
	a.svcAvailable = available
	a.mu.Unlock()
	return available
}

// ServiceAvailable сообщает фронтенду, доступна ли служба: по этому признаку
// интерфейс либо предлагает TUN сразу, либо просит перезапуск от админа.
func (a *App) ServiceAvailable() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.svcAvailable
}

// serviceClient возвращает живое соединение со службой, устанавливая его при
// необходимости. Единственный потребитель — serviceBackend.
func (a *App) serviceClient() (*ipc.Client, error) {
	a.mu.Lock()
	if a.svc != nil {
		client := a.svc
		a.mu.Unlock()
		return client, nil
	}
	a.mu.Unlock()

	client, err := ipc.Dial(serviceDialTimeout)
	if err != nil {
		a.mu.Lock()
		a.svcAvailable = false
		a.mu.Unlock()
		return nil, err
	}

	a.mu.Lock()
	// Пока мы подключались, соединение мог установить другой вызов.
	if a.svc != nil {
		existing := a.svc
		a.mu.Unlock()
		_ = client.Close()
		return existing, nil
	}
	a.svc = client
	a.svcAvailable = true
	a.mu.Unlock()

	go a.forwardServiceEvents(client)
	return client, nil
}

// forwardServiceEvents переливает события службы в тот же поток, что и
// события локального менеджера: журнал, трей и фронтенд не должны знать,
// кто именно держит туннель.
func (a *App) forwardServiceEvents(client *ipc.Client) {
	for ev := range client.Events() {
		a.handleEvent(ev.TunnelID, sslcon.EventType(ev.Type), ev.Message)
	}

	// Канал закрыт — служба остановлена или связь оборвалась.
	a.mu.Lock()
	if a.svc == client {
		a.svc = nil
		a.svcAvailable = false
	}
	a.mu.Unlock()
	a.log("warn", "связь со службой DualVPN потеряна")
}

// backend выбирает исполнителя команд под текущий режим работы.
// В TUN-режиме служба предпочтительна: она уже держит права, и приложению
// не нужно перезапускаться от администратора.
func (a *App) backend() tunnelBackend {
	a.mu.Lock()
	m := a.mode
	available := a.svcAvailable
	a.mu.Unlock()

	if m == mode.TUN && available {
		return &serviceBackend{app: a}
	}
	return &localBackend{app: a}
}

// localBackend — команды исполняет менеджер внутри приложения.
type localBackend struct{ app *App }

func (b *localBackend) Connect(id string) error {
	return b.app.manager.Start(b.app.context(), id)
}
func (b *localBackend) Disconnect(id string) error   { return b.app.manager.Stop(id) }
func (b *localBackend) DisconnectAll()               { b.app.manager.StopAll() }
func (b *localBackend) Submit2FA(id, c string) error { return b.app.manager.Submit2FA(id, c) }
func (b *localBackend) Status(id string) (bool, string) {
	return b.app.manager.Status(id)
}

// serviceBackend — команды исполняет служба DualVPN.
type serviceBackend struct{ app *App }

// Connect передаёт службе параметры туннеля из конфигурации: служба их не
// хранит, поэтому учётные данные уходят на время сеанса при каждом запуске.
func (b *serviceBackend) Connect(id string) error {
	tun, ok := b.app.tunnelByName(id)
	if !ok {
		return fmt.Errorf("туннель %q не найден в конфигурации", id)
	}
	client, err := b.app.serviceClient()
	if err != nil {
		return err
	}
	return client.Connect(ipc.ConnectParams{
		ID:       tun.Name,
		Host:     tun.Endpoint,
		Group:    tun.Group,
		Username: tun.Username,
		Password: tun.Password,
		TunName:  tun.TunName,
		Routes:   tun.Routes,
	})
}

func (b *serviceBackend) Disconnect(id string) error {
	client, err := b.app.serviceClient()
	if err != nil {
		return err
	}
	return client.Disconnect(id)
}

func (b *serviceBackend) DisconnectAll() {
	client, err := b.app.serviceClient()
	if err != nil {
		b.app.log("err", err.Error())
		return
	}
	if err := client.DisconnectAll(); err != nil {
		b.app.log("err", err.Error())
	}
}

func (b *serviceBackend) Submit2FA(id, code string) error {
	client, err := b.app.serviceClient()
	if err != nil {
		return err
	}
	return client.Submit2FA(id, code)
}

// Status спрашивает состояние у службы. Ошибка связи означает «не подключён»:
// показывать состояние важнее, чем падать из-за недоступной службы.
func (b *serviceBackend) Status(id string) (bool, string) {
	client, err := b.app.serviceClient()
	if err != nil {
		return false, ""
	}
	states, err := client.Status()
	if err != nil {
		return false, ""
	}
	for _, st := range states {
		if st.ID == id {
			return st.Connected, mode.TUN
		}
	}
	return false, ""
}

// tunnelByName возвращает туннель из конфигурации по имени.
func (a *App) tunnelByName(name string) (config.Tunnel, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, t := range a.cfg.Tunnels {
		if t.Name == name {
			return t, true
		}
	}
	return config.Tunnel{}, false
}
