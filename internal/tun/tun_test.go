package tun

import "testing"

// TestTunConfigValidate — валидация конфигурации TUN-адаптера.
func TestTunConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "валидный конфиг",
			cfg:  Config{Name: "tun0", Address: "10.8.0.2", MTU: 1400},
		},
		{
			name:    "пустое имя",
			cfg:     Config{Name: "", Address: "10.8.0.2", MTU: 1400},
			wantErr: true,
		},
		{
			name:    "невалидный адрес",
			cfg:     Config{Name: "tun0", Address: "не-адрес", MTU: 1400},
			wantErr: true,
		},
		{
			name:    "пустой адрес",
			cfg:     Config{Name: "tun0", Address: "", MTU: 1400},
			wantErr: true,
		},
		{
			name:    "нулевой MTU",
			cfg:     Config{Name: "tun0", Address: "10.8.0.2", MTU: 0},
			wantErr: true,
		},
		{
			name:    "отрицательный MTU",
			cfg:     Config{Name: "tun0", Address: "10.8.0.2", MTU: -1},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr && err == nil {
				t.Errorf("Validate(%+v): ожидалась ошибка, получен nil", tt.cfg)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate(%+v): неожиданная ошибка: %v", tt.cfg, err)
			}
		})
	}
}

// TestParseAddress — разбор IPv4-адреса TUN-интерфейса.
func TestParseAddress(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{name: "валидный адрес", addr: "10.8.0.2"},
		{name: "мусор", addr: "invalid", wantErr: true},
		{name: "пустая строка", addr: "", wantErr: true},
		{name: "октет вне диапазона", addr: "10.8.0.999", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip, err := ParseAddress(tt.addr)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseAddress(%q): ожидалась ошибка, получено %v", tt.addr, ip)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAddress(%q): неожиданная ошибка: %v", tt.addr, err)
			}
			if ip.String() != tt.addr {
				t.Errorf("ParseAddress(%q) = %v, ожидалось %q", tt.addr, ip, tt.addr)
			}
		})
	}
}
