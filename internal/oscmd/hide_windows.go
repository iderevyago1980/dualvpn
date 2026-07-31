//go:build windows

package oscmd

import (
	"os/exec"
	"syscall"
)

// createNoWindow — флаг CreateProcess (CREATE_NO_WINDOW), подавляющий
// создание окна консоли для дочернего процесса.
const createNoWindow = 0x08000000

// hide прячет окно консоли дочернего процесса. Приложение собрано как
// windowsgui; без этого каждый вызов netsh/route/powershell мигал бы чёрным
// окном поверх интерфейса.
func hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
