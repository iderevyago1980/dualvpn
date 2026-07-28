package routing

import (
	"reflect"
	"testing"
)

// TestParseCIDR — разбор CIDR в пару (сеть, маска).
func TestParseCIDR(t *testing.T) {
	tests := []struct {
		name    string
		cidr    string
		network string
		mask    string
		wantErr bool
	}{
		{name: "валидный /24", cidr: "192.168.1.0/24", network: "192.168.1.0", mask: "255.255.255.0"},
		{name: "валидный /16", cidr: "10.0.0.0/16", network: "10.0.0.0", mask: "255.255.0.0"},
		{name: "хост /32", cidr: "172.16.5.1/32", network: "172.16.5.1", mask: "255.255.255.255"},
		{name: "адрес не на границе сети", cidr: "192.168.1.77/24", network: "192.168.1.0", mask: "255.255.255.0"},
		{name: "мусор", cidr: "not-a-cidr", wantErr: true},
		{name: "пустая строка", cidr: "", wantErr: true},
		{name: "префикс /33", cidr: "192.168.1.0/33", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			network, mask, err := ParseCIDR(tt.cidr)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseCIDR(%q): ожидалась ошибка, получено network=%q mask=%q", tt.cidr, network, mask)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCIDR(%q): неожиданная ошибка: %v", tt.cidr, err)
			}
			if network != tt.network || mask != tt.mask {
				t.Errorf("ParseCIDR(%q) = (%q, %q), ожидалось (%q, %q)", tt.cidr, network, mask, tt.network, tt.mask)
			}
		})
	}
}

// TestBuildAddRouteCommand — формирование команды route add для linux/windows.
func TestBuildAddRouteCommand(t *testing.T) {
	tests := []struct {
		name string
		os   string
		want []string
	}{
		{
			name: "linux",
			os:   "linux",
			want: []string{"route", "add", "-net", "192.168.1.0", "netmask", "255.255.255.0", "gw", "10.8.0.1", "dev", "tun0"},
		},
		{
			// route add ... IF <имя> невалидна: IF принимает индекс интерфейса.
			name: "windows",
			os:   "windows",
			want: []string{"netsh", "interface", "ipv4", "add", "route",
				"prefix=192.168.1.0/24", "interface=tun0", "store=active"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildAddRouteCommand(tt.os, "192.168.1.0/24", "10.8.0.1", "tun0")
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("BuildAddRouteCommand(%q) = %v, ожидалось %v", tt.os, got, tt.want)
			}
		})
	}
}

// TestBuildDeleteRouteCommand — формирование команды route delete для linux/windows.
func TestBuildDeleteRouteCommand(t *testing.T) {
	tests := []struct {
		name string
		os   string
		want []string
	}{
		{
			name: "linux",
			os:   "linux",
			want: []string{"route", "del", "-net", "192.168.1.0", "netmask", "255.255.255.0", "gw", "10.8.0.1", "dev", "tun0"},
		},
		{
			name: "windows",
			os:   "windows",
			want: []string{"netsh", "interface", "ipv4", "delete", "route",
				"prefix=192.168.1.0/24", "interface=tun0", "store=active"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildDeleteRouteCommand(tt.os, "192.168.1.0/24", "10.8.0.1", "tun0")
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("BuildDeleteRouteCommand(%q) = %v, ожидалось %v", tt.os, got, tt.want)
			}
		})
	}
}
