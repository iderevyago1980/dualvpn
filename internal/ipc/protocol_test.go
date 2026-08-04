package ipc

import (
	"strings"
	"testing"
)

// TestConnectParamsAcceptsRealNames — проверка не должна мешать обычной
// работе: имена туннелей и групп бывают русскими и с пробелами.
func TestConnectParamsAcceptsRealNames(t *testing.T) {
	cases := []ConnectParams{
		{ID: "Туннель 1", Host: "vpn.example.com", TunName: "dualvpn0"},
		{ID: "MT", Host: "vpn2.example.com", Group: "Remote Access", TunName: "vpn2"},
		{ID: "Офис (резерв)", Host: "10.0.0.1:8443", Group: "Основная"},
		{ID: "VPN-1", Host: "vpn1.example.com", Group: "Group-2FA",
			Username: "user", Password: "SD23fg45zzZ!", Routes: []string{"10.0.0.0/8"}},
	}
	for _, p := range cases {
		if err := p.Validate(); err != nil {
			t.Errorf("отвергнуты допустимые параметры %q: %v", p.ID, err)
		}
	}
}

// TestConnectParamsRejectsInjection — служба работает под LocalSystem, а
// запрос приходит от обычного пользователя: строки, которыми можно
// вклиниться в команду netsh/route или в XML-запрос, должны отсекаться.
func TestConnectParamsRejectsInjection(t *testing.T) {
	base := ConnectParams{ID: "Офис", Host: "vpn.example.com", TunName: "dualvpn0"}

	cases := []struct {
		name  string
		spoil func(*ConnectParams)
	}{
		{"имя интерфейса с пробелом и командой", func(p *ConnectParams) { p.TunName = "dualvpn0 & calc" }},
		{"имя интерфейса с кавычкой", func(p *ConnectParams) { p.TunName = `dual"vpn` }},
		{"имя интерфейса с путём", func(p *ConnectParams) { p.TunName = `..\..\system32` }},
		{"адрес с пробелом", func(p *ConnectParams) { p.Host = "vpn.example.com & calc" }},
		{"адрес со слэшем", func(p *ConnectParams) { p.Host = "vpn.example.com/../x" }},
		{"маршрут с командой", func(p *ConnectParams) { p.Routes = []string{"10.0.0.0/8 & calc"} }},
		{"группа с XML", func(p *ConnectParams) { p.Group = "<script>" }},
		{"имя туннеля с переводом строки", func(p *ConnectParams) { p.ID = "Офис\nсекрет" }},
		{"пустое имя туннеля", func(p *ConnectParams) { p.ID = "" }},
		{"пустой адрес", func(p *ConnectParams) { p.Host = "" }},
		{"перевод строки в пароле", func(p *ConnectParams) { p.Password = "pass\nword" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.spoil(&p)
			if err := p.Validate(); err == nil {
				t.Errorf("приняты опасные параметры: %+v", p)
			}
		})
	}
}

// TestTwoFAValidation — код второго фактора уходит в XML-запрос к шлюзу.
func TestTwoFAValidation(t *testing.T) {
	if err := (TwoFAParams{ID: "Офис", Code: "123456"}).Validate(); err != nil {
		t.Errorf("отвергнут обычный код: %v", err)
	}
	bad := []TwoFAParams{
		{ID: "Офис", Code: ""},
		{ID: "Офис", Code: "<inject>"},
		{ID: "Офис", Code: strings.Repeat("1", 33)},
		{ID: "", Code: "123456"},
	}
	for _, p := range bad {
		if err := p.Validate(); err == nil {
			t.Errorf("принят недопустимый код: %+v", p)
		}
	}
}

// TestLongValuesRejected — служба привилегированная, память ей тратить
// впустую нельзя.
func TestLongValuesRejected(t *testing.T) {
	p := ConnectParams{
		ID:      strings.Repeat("и", 65),
		Host:    "vpn.example.com",
		TunName: "dualvpn0",
	}
	if err := p.Validate(); err == nil {
		t.Error("принято слишком длинное имя туннеля")
	}

	p = ConnectParams{ID: "Офис", Host: "vpn.example.com", Password: strings.Repeat("x", 257)}
	if err := p.Validate(); err == nil {
		t.Error("принят слишком длинный пароль")
	}
}
