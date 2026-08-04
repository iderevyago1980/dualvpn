//go:build !windows

package main

import "errors"

// dispatch на не-Windows платформах не поддерживается: служба нужна ради
// прав администратора в Windows, на Linux эту роль играет sudo.
func dispatch(string) error {
	return errors.New("служба DualVPN поддерживается только на Windows (на Linux используйте sudo)")
}
