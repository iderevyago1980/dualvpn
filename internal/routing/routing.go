// Package routing — управление маршрутами ОС для split-tunnel режима.
//
// Каждому туннелю назначается свой набор подсетей: пакеты к ним направляются
// через соответствующий TUN-интерфейс командой route add/delete.
package routing

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
)

// ParseCIDR разбирает CIDR-нотацию (например, "192.168.1.0/24")
// в адрес сети и маску в dotted-decimal виде ("192.168.1.0", "255.255.255.0").
func ParseCIDR(cidr string) (network, mask string, err error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", "", fmt.Errorf("некорректный CIDR %q: %w", cidr, err)
	}
	// IP-адрес сети (уже обрезан по маске самим ParseCIDR).
	network = ipNet.IP.String()
	// IPMask хранится как байты — конвертируем в dotted-decimal.
	mask = net.IP(ipNet.Mask).String()
	return network, mask, nil
}

// BuildAddRouteCommand формирует команду добавления маршрута для указанной ОС.
// Возвращает nil при некорректном CIDR или неподдерживаемой ОС.
func BuildAddRouteCommand(goos, cidr, gateway, iface string) []string {
	network, mask, err := ParseCIDR(cidr)
	if err != nil {
		return nil
	}
	switch goos {
	case "linux":
		return []string{"route", "add", "-net", network, "netmask", mask, "gw", gateway, "dev", iface}
	case "windows":
		return []string{"route", "add", network, "mask", mask, gateway, "IF", iface}
	}
	return nil
}

// BuildDeleteRouteCommand формирует команду удаления маршрута для указанной ОС.
// Возвращает nil при некорректном CIDR или неподдерживаемой ОС.
func BuildDeleteRouteCommand(goos, cidr, gateway, iface string) []string {
	network, mask, err := ParseCIDR(cidr)
	if err != nil {
		return nil
	}
	switch goos {
	case "linux":
		return []string{"route", "del", "-net", network, "netmask", mask, "gw", gateway, "dev", iface}
	case "windows":
		return []string{"route", "delete", network, "mask", mask, gateway, "IF", iface}
	}
	return nil
}

// AddRoute добавляет маршрут к подсети cidr через gateway на интерфейсе iface.
// Требует прав администратора (route add).
func AddRoute(cidr, gateway, iface string) error {
	return runRoute(BuildAddRouteCommand(runtime.GOOS, cidr, gateway, iface), cidr)
}

// DeleteRoute удаляет маршрут к подсети cidr.
func DeleteRoute(cidr, gateway, iface string) error {
	return runRoute(BuildDeleteRouteCommand(runtime.GOOS, cidr, gateway, iface), cidr)
}

// DeleteAllRoutes удаляет все перечисленные маршруты; ошибки по отдельным
// маршрутам накапливаются, чтобы попытаться снять максимум маршрутов.
func DeleteAllRoutes(routes []string, gateway, iface string) error {
	var errs []string
	for _, cidr := range routes {
		if err := DeleteRoute(cidr, gateway, iface); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("удаление маршрутов: %s", strings.Join(errs, "; "))
	}
	return nil
}

// runRoute выполняет собранную команду route через exec.
func runRoute(argv []string, cidr string) error {
	if argv == nil {
		return fmt.Errorf("не удалось собрать команду маршрута для %q (ОС %s)", cidr, runtime.GOOS)
	}
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w (%s)", strings.Join(argv, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
