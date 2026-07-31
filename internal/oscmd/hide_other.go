//go:build !windows

package oscmd

import "os/exec"

// hide — на не-Windows платформах окна консоли нет, подавлять нечего.
func hide(_ *exec.Cmd) {}
