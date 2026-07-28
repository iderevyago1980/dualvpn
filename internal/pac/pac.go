// Package pac — автонастройка прокси для браузера (PAC-файл).
//
// В режиме SOCKS5 каждый туннель поднимает свой прокси, и вручную браузер
// можно направить только в один из них. PAC решает это: скрипт сам выбирает
// туннель по имени хоста (зоны split-DNS шлюза) или по адресу (подсети
// split-include), а весь прочий трафик отправляет напрямую.
//
// Имена намеренно сопоставляются только по суффиксу домена: вызов
// dnsResolve() в PAC разрешал бы имя системным (публичным) DNS — то есть
// именно тем способом, который для корпоративных зон не работает и утекает
// наружу. Разрешение имени выполняет уже сам SOCKS5-прокси, внутри туннеля.
package pac

import (
	"fmt"
	"net"
	"strings"
)

// Tunnel — данные одного туннеля для правил PAC.
type Tunnel struct {
	Name      string   // отображаемое имя (попадает в комментарий скрипта)
	SocksPort int      // порт локального SOCKS5-прокси
	Domains   []string // зоны split-DNS (X-CSTP-Split-DNS)
	Subnets   []string // подсети split-include: "10.0.0.0/255.255.0.0" или CIDR
}

// Script собирает PAC-скрипт для набора туннелей. Туннели без порта или без
// правил пропускаются: пустое правило всё равно ничего не направляет.
func Script(tunnels []Tunnel) string {
	var b strings.Builder
	b.WriteString("// DualVPN — автонастройка прокси. Файл создаётся приложением\n")
	b.WriteString("// из параметров, полученных от VPN-шлюзов при подключении.\n")
	b.WriteString("function FindProxyForURL(url, host) {\n")
	b.WriteString("    host = host.toLowerCase();\n")
	b.WriteString("    var isIP = /^\\d{1,3}(\\.\\d{1,3}){3}$/.test(host);\n\n")

	for _, t := range tunnels {
		if t.SocksPort <= 0 {
			continue
		}
		proxy := fmt.Sprintf("SOCKS5 127.0.0.1:%d; SOCKS 127.0.0.1:%d", t.SocksPort, t.SocksPort)

		domains := normalizeDomains(t.Domains)
		subnets := normalizeSubnets(t.Subnets)
		if len(domains) == 0 && len(subnets) == 0 {
			continue
		}

		fmt.Fprintf(&b, "    // %s\n", t.Name)
		for _, d := range domains {
			// Точное совпадение и любой поддомен.
			fmt.Fprintf(&b, "    if (host === %q || dnsDomainIs(host, %q)) return %q;\n",
				d, "."+d, proxy)
		}
		for _, s := range subnets {
			// Подсети применимы только к литеральным адресам: для имени
			// isInNet вызвал бы системный DNS.
			fmt.Fprintf(&b, "    if (isIP && isInNet(host, %q, %q)) return %q;\n",
				s.network, s.mask, proxy)
		}
		b.WriteString("\n")
	}

	b.WriteString("    return \"DIRECT\";\n}\n")
	return b.String()
}

// normalizeDomains приводит зоны к виду "corp.example" (без ведущей точки,
// в нижнем регистре) и отбрасывает пустые и обратные зоны in-addr.arpa —
// они нужны обратному разрешению адресов, а не выбору прокси.
func normalizeDomains(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, d := range in {
		d = strings.ToLower(strings.Trim(strings.TrimSpace(d), "."))
		if d == "" || strings.HasSuffix(d, "in-addr.arpa") {
			continue
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}

type subnet struct{ network, mask string }

// normalizeSubnets принимает как формат шлюза ("10.0.0.0/255.255.0.0"), так
// и обычный CIDR ("10.0.0.0/16") и приводит оба к паре сеть+маска, которую
// ожидает isInNet.
func normalizeSubnets(in []string) []subnet {
	seen := make(map[subnet]struct{}, len(in))
	out := make([]subnet, 0, len(in))
	for _, raw := range in {
		s, ok := parseSubnet(strings.TrimSpace(raw))
		if !ok {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func parseSubnet(raw string) (subnet, bool) {
	ip, maskPart, found := strings.Cut(raw, "/")
	if !found {
		return subnet{}, false
	}
	addr := net.ParseIP(strings.TrimSpace(ip))
	if addr == nil || addr.To4() == nil {
		return subnet{}, false
	}

	// Формат шлюза: маска записана адресом.
	if m := net.ParseIP(strings.TrimSpace(maskPart)); m != nil && m.To4() != nil {
		return subnet{network: addr.To4().String(), mask: m.To4().String()}, true
	}

	// Обычный CIDR.
	_, ipNet, err := net.ParseCIDR(raw)
	if err != nil {
		return subnet{}, false
	}
	return subnet{
		network: ipNet.IP.String(),
		mask:    net.IP(ipNet.Mask).String(),
	}, true
}
