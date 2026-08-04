//go:build windows

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"dualvpn/internal/ipc"
	"dualvpn/internal/vpn"
)

// dispatch разбирает команду запуска.
func dispatch(cmd string) error {
	switch cmd {
	case "install":
		return install()
	case "uninstall":
		return uninstall()
	case "run":
		return runConsole()
	case "":
		return runService()
	default:
		return fmt.Errorf("неизвестная команда %q (install | uninstall | run)", cmd)
	}
}

// install регистрирует службу и запускает её. Требует прав администратора —
// это единственный момент, когда они нужны.
func install() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("путь к исполняемому файлу: %w", err)
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("диспетчер служб (нужны права администратора): %w", err)
	}
	defer m.Disconnect() //nolint:errcheck // соединение закрывается при выходе

	if s, err := m.OpenService(serviceName); err == nil {
		s.Close() //nolint:errcheck // служба уже есть — просто сообщаем
		return fmt.Errorf("служба %s уже установлена", serviceName)
	}

	s, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName: serviceDisplay,
		Description: serviceDesc,
		// Автозапуск: туннели должны подниматься по требованию пользователя
		// в любой момент, а не только после ручного старта службы.
		StartType: mgr.StartAutomatic,
	})
	if err != nil {
		return fmt.Errorf("создание службы: %w", err)
	}
	defer s.Close() //nolint:errcheck

	// Журнал событий: без регистрации источника записи службы попадают в
	// журнал с пометкой о неизвестном источнике.
	if err := eventlog.InstallAsEventCreate(serviceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
		// Не критично: служба работает и без своего источника в журнале.
		fmt.Fprintln(os.Stderr, "предупреждение: источник журнала событий не зарегистрирован:", err)
	}

	if err := s.Start(); err != nil {
		return fmt.Errorf("запуск службы: %w", err)
	}
	fmt.Println("служба", serviceName, "установлена и запущена")
	return nil
}

// uninstall останавливает и удаляет службу.
func uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("диспетчер служб (нужны права администратора): %w", err)
	}
	defer m.Disconnect() //nolint:errcheck

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("служба %s не установлена", serviceName)
	}
	defer s.Close() //nolint:errcheck

	// Остановку ждём: удаление незавершённой службы оставляет её в списке
	// до перезагрузки.
	if status, err := s.Control(svc.Stop); err == nil {
		deadline := time.Now().Add(20 * time.Second)
		for status.State != svc.Stopped && time.Now().Before(deadline) {
			time.Sleep(300 * time.Millisecond)
			if status, err = s.Query(); err != nil {
				break
			}
		}
	}

	if err := s.Delete(); err != nil {
		return fmt.Errorf("удаление службы: %w", err)
	}
	_ = eventlog.Remove(serviceName) // источник журнала может быть не зарегистрирован
	fmt.Println("служба", serviceName, "удалена")
	return nil
}

// runConsole выполняет службу в консоли: тот же код, но с выводом на экран.
// Нужен для диагностики — запускать от администратора.
func runConsole() error {
	fmt.Println("DualVPN service", version, "— консольный режим, Ctrl+C для выхода")
	engine, err := startEngine(logfPrintln)
	if err != nil {
		return err
	}
	defer engine.stop()

	select {} //nolint:staticcheck // консольный режим завершается по Ctrl+C
}

// runService запускает службу под управлением диспетчера служб Windows.
func runService() error {
	elog, err := eventlog.Open(serviceName)
	if err != nil {
		// Журнал недоступен — не повод не работать.
		return svc.Run(serviceName, &service{logf: func(string, ...any) {}})
	}
	defer elog.Close() //nolint:errcheck

	logf := func(format string, args ...any) {
		_ = elog.Info(1, fmt.Sprintf(format, args...))
	}
	return svc.Run(serviceName, &service{logf: logf})
}

func logfPrintln(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
}

// service реализует svc.Handler.
type service struct {
	logf func(string, ...any)
}

// Execute — жизненный цикл службы: поднять канал и менеджер туннелей,
// дождаться команды остановки, всё закрыть.
func (s *service) Execute(_ []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}

	engine, err := startEngine(s.logf)
	if err != nil {
		s.logf("не удалось запустить службу: %v", err)
		changes <- svc.Status{State: svc.Stopped}
		return true, 1
	}
	changes <- svc.Status{State: svc.Running, Accepts: accepted}
	s.logf("служба DualVPN %s запущена, канал %s", version, ipc.PipeName)

	for c := range r {
		switch c.Cmd {
		case svc.Interrogate:
			changes <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			changes <- svc.Status{State: svc.StopPending}
			engine.stop()
			changes <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
	return false, 0
}

// engine — работающая связка «менеджер туннелей + канал управления».
type engine struct {
	mgr    *vpn.Manager
	ln     net.Listener
	cancel context.CancelFunc
}

// startEngine поднимает менеджер туннелей и слушает именованный канал.
func startEngine(logf func(string, ...any)) (*engine, error) {
	ln, err := ipc.Listen()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	mgr := vpn.NewManager()
	srv := ipc.NewServer(newHandler(ctx, mgr))
	srv.Logf = logf

	go forwardEvents(mgr, srv)
	go func() {
		if err := srv.Serve(ln); err != nil {
			logf("канал управления закрыт: %v", err)
		}
	}()

	return &engine{mgr: mgr, ln: ln, cancel: cancel}, nil
}

// stop останавливает туннели и закрывает канал: адаптеры, маршруты и
// правила DNS должны сниматься при остановке службы, а не переживать её.
func (e *engine) stop() {
	e.mgr.StopAll()
	e.cancel()
	_ = e.ln.Close()
}
