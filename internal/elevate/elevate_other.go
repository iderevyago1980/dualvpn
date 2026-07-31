//go:build !windows

package elevate

import "errors"

// Relaunch на не-Windows платформах не поддерживается: там повышение прав
// делается запуском через sudo, а не средствами приложения.
func Relaunch() error {
	return errors.New("elevate: перезапуск с повышением прав поддерживается только на Windows — используйте sudo")
}
