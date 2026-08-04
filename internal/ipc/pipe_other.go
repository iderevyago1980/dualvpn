//go:build !windows

package ipc

import (
	"errors"
	"net"
	"time"
)

// errUnsupported — служба с именованным каналом существует только на
// Windows: на Linux права даёт sudo, отдельный посредник не нужен.
var errUnsupported = errors.New("ipc: служба поддерживается только на Windows")

// Listen на не-Windows платформах не поддерживается.
func Listen() (net.Listener, error) { return nil, errUnsupported }

// Dial на не-Windows платформах не поддерживается.
func Dial(time.Duration) (*Client, error) { return nil, errUnsupported }

// Available на не-Windows платформах всегда false.
func Available() bool { return false }
