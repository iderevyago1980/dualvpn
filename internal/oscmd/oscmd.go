// Package oscmd — запуск внешних консольных утилит (netsh, route, ip,
// powershell) с общими для всего приложения гарантиями:
//
//   - таймаут на выполнение: зависшая команда не должна вешать подключение
//     или отключение туннеля навсегда;
//   - на Windows — подавление всплывающего окна консоли. Приложение собрано
//     с -H=windowsgui, и без флага CREATE_NO_WINDOW каждый дочерний процесс
//     мигал бы чёрным окном поверх интерфейса.
package oscmd

import (
	"context"
	"os/exec"
	"time"
)

// DefaultTimeout — таймаут для быстрых команд настройки сети (netsh, route,
// ip). Их штатное время — доли секунды; секунды означают, что команда
// зависла и её пора снимать.
const DefaultTimeout = 30 * time.Second

// Run выполняет команду name с аргументами args и возвращает объединённый
// вывод stdout+stderr. По истечении timeout процесс принудительно
// завершается (нулевой timeout — без ограничения по времени). На Windows
// окно консоли дочернего процесса подавляется.
func Run(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	hide(cmd)
	return cmd.CombinedOutput()
}
