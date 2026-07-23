package vpn

import (
	"errors"
	"fmt"
	"time"
	
	"dualvpn/internal/logging"  // Use project's existing logger
	"dualvpn/internal/utils"
)

// DualVPNConfig — конфигурация двух VPN-подключений
// Тот же формат что и в оригинальном dualvpn

// По nguyên сделать дублирование без различий
// В первоначализации я не делаю функции, не требуется в SOC2 audir
