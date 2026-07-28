package sslcon

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Ответ сервера на init-запрос: форма аутентификации с группами.
const initResponse = `<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="auth-request" aggregate-auth-version="2">
    <opaque is-for="sg">
        <tunnel-group>TG-Basic</tunnel-group>
        <group-alias>Basic</group-alias>
        <config-hash>1234567890</config-hash>
    </opaque>
    <auth id="main">
        <message>Please enter your username and password.</message>
        <form method="post" action="/+webvpn+/index.html">
            <select name="group_list">
                <option>Basic</option>
                <option>Partners</option>
            </select>
        </form>
    </auth>
</config-auth>`

// Ответ сервера на auth-reply: успех с session-token в XML.
const completeTokenResponse = `<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="complete" aggregate-auth-version="2">
    <session-token>TOKEN123</session-token>
</config-auth>`

// Ответ сервера на auth-reply: успех без токена в XML (совместимость с
// ocserv — токен приходит в cookie webvpn).
const completeCookieResponse = `<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="complete" aggregate-auth-version="2">
</config-auth>`

// newMockASA поднимает мок Cisco ASA: TLS-сервер, различающий init и
// auth-reply по телу запроса. Записывает полученные тела в requests.
func newMockASA(t *testing.T, authReplyHandler func(w http.ResponseWriter)) (*httptest.Server, *[]string, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var requests []string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, string(body))
		mu.Unlock()

		w.Header().Set("Content-Type", "text/xml")
		switch {
		case strings.Contains(string(body), `type="init"`):
			fmt.Fprint(w, initResponse)
		case strings.Contains(string(body), `type="auth-reply"`):
			authReplyHandler(w)
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &requests, &mu
}

// newTestClient создаёт клиент, указывающий на мок-сервер.
func newTestClient(ts *httptest.Server, group string) *Client {
	host := strings.TrimPrefix(ts.URL, "https://")
	return NewClient(ClientConfig{
		Host:               host,
		Username:           "user",
		Password:           "pass",
		Group:              group,
		InsecureSkipVerify: true, // самоподписанный сертификат httptest
	})
}

func TestNewClient(t *testing.T) {
	c := NewClient(ClientConfig{
		Host:               "vpn.example.com",
		Username:           "alice",
		Password:           "secret",
		Group:              "Basic",
		SecretKey:          "key=1",
		InsecureSkipVerify: true,
	})

	if c.Prof == nil {
		t.Fatal("Prof не инициализирован")
	}
	if c.Prof.Host != "vpn.example.com" {
		t.Errorf("Host = %q, ожидалось vpn.example.com", c.Prof.Host)
	}
	if c.Prof.HostWithPort != "vpn.example.com:443" {
		t.Errorf("HostWithPort = %q, ожидалось vpn.example.com:443", c.Prof.HostWithPort)
	}
	if c.Prof.Scheme != "https://" {
		t.Errorf("Scheme = %q, ожидалось https://", c.Prof.Scheme)
	}
	if c.Prof.Username != "alice" || c.Prof.Password != "secret" {
		t.Error("Username/Password не переданы в Profile")
	}
	if c.Prof.Group != "Basic" || c.Prof.SecretKey != "key=1" {
		t.Error("Group/SecretKey не переданы в Profile")
	}
	if !c.insecureSkipVerify {
		t.Error("insecureSkipVerify не передан в Client")
	}
	if c.Prof.ComputerName == "" || c.Prof.DeviceType == "" {
		t.Error("сведения об устройстве не заполнены")
	}

	// Явный порт не должен дублироваться
	c2 := NewClient(ClientConfig{Host: "vpn.example.com:8443", Username: "u", Password: "p"})
	if c2.Prof.HostWithPort != "vpn.example.com:8443" {
		t.Errorf("HostWithPort = %q, ожидалось vpn.example.com:8443", c2.Prof.HostWithPort)
	}
}

func TestClientConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ClientConfig
		wantErr bool
	}{
		{"валидный", ClientConfig{Host: "h", Username: "u", Password: "p"}, false},
		{"без host", ClientConfig{Username: "u", Password: "p"}, true},
		{"без username", ClientConfig{Host: "h", Password: "p"}, true},
		{"без password", ClientConfig{Host: "h", Username: "u"}, true},
		{"пустой", ClientConfig{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}

	// Profile.Validate — те же правила
	if err := NewProfile("h", "u", "p", "", "").Validate(); err != nil {
		t.Errorf("Profile.Validate() на валидном профиле: %v", err)
	}
	if err := NewProfile("", "u", "p", "", "").Validate(); err == nil {
		t.Error("Profile.Validate() без host должен вернуть ошибку")
	}
}

func TestClientInitAuth(t *testing.T) {
	ts, requests, mu := newMockASA(t, func(w http.ResponseWriter) {
		fmt.Fprint(w, completeTokenResponse)
	})

	c := newTestClient(ts, "Basic")
	defer c.Close()

	if err := c.InitAuth(); err != nil {
		t.Fatalf("InitAuth: %v", err)
	}

	// Сервер должен получить XML init-запрос
	mu.Lock()
	if len(*requests) != 1 {
		t.Fatalf("получено %d запросов, ожидался 1", len(*requests))
	}
	initReq := (*requests)[0]
	mu.Unlock()
	if !strings.Contains(initReq, `type="init"`) {
		t.Errorf("init-запрос не содержит type=\"init\": %s", initReq)
	}
	if !strings.Contains(initReq, "<config-auth") {
		t.Errorf("init-запрос не является config-auth XML: %s", initReq)
	}

	// Данные из ответа сервера должны попасть в Profile
	if c.Prof.AuthPath != "/+webvpn+/index.html" {
		t.Errorf("AuthPath = %q, ожидалось /+webvpn+/index.html", c.Prof.AuthPath)
	}
	if c.Prof.TunnelGroup != "TG-Basic" {
		t.Errorf("TunnelGroup = %q, ожидалось TG-Basic", c.Prof.TunnelGroup)
	}
	if c.Prof.GroupAlias != "Basic" {
		t.Errorf("GroupAlias = %q, ожидалось Basic", c.Prof.GroupAlias)
	}
	if c.Prof.ConfigHash != "1234567890" {
		t.Errorf("ConfigHash = %q, ожидалось 1234567890", c.Prof.ConfigHash)
	}
	if !c.Prof.Initialized {
		t.Error("Prof.Initialized должен быть true после InitAuth")
	}
}

func TestClientInitAuthUnknownGroup(t *testing.T) {
	ts, _, _ := newMockASA(t, func(w http.ResponseWriter) {
		fmt.Fprint(w, completeTokenResponse)
	})

	c := newTestClient(ts, "НетТакойГруппы")
	defer c.Close()

	err := c.InitAuth()
	if err == nil {
		t.Fatal("InitAuth с неизвестной группой должен вернуть ошибку")
	}
	// В тексте ошибки должны быть и отвергнутая группа, и список доступных —
	// без этого пользователю нечем исправить конфиг.
	if !strings.Contains(err.Error(), "НетТакойГруппы") ||
		!strings.Contains(err.Error(), "Basic") || !strings.Contains(err.Error(), "Partners") {
		t.Errorf("неожиданная ошибка: %v", err)
	}
}

func TestClientPasswordAuth(t *testing.T) {
	t.Run("session-token в XML", func(t *testing.T) {
		ts, requests, mu := newMockASA(t, func(w http.ResponseWriter) {
			fmt.Fprint(w, completeTokenResponse)
		})

		c := newTestClient(ts, "Basic")
		defer c.Close()

		if err := c.InitAuth(); err != nil {
			t.Fatalf("InitAuth: %v", err)
		}
		if err := c.PasswordAuth(); err != nil {
			t.Fatalf("PasswordAuth: %v", err)
		}

		// auth-reply должен содержать учётные данные и группу
		mu.Lock()
		if len(*requests) != 2 {
			t.Fatalf("получено %d запросов, ожидалось 2", len(*requests))
		}
		authReq := (*requests)[1]
		mu.Unlock()
		for _, want := range []string{"<username>user</username>", "<password>pass</password>", "<group-select>Basic</group-select>", "<tunnel-group>TG-Basic</tunnel-group>"} {
			if !strings.Contains(authReq, want) {
				t.Errorf("auth-reply не содержит %s", want)
			}
		}

		if c.SessionToken != "TOKEN123" {
			t.Errorf("SessionToken = %q, ожидалось TOKEN123", c.SessionToken)
		}
		if c.Cookie() != "TOKEN123" {
			t.Errorf("Cookie() = %q, ожидалось TOKEN123", c.Cookie())
		}
	})

	t.Run("webvpn cookie (совместимость с ocserv)", func(t *testing.T) {
		ts, _, _ := newMockASA(t, func(w http.ResponseWriter) {
			http.SetCookie(w, &http.Cookie{Name: "webvpn", Value: "COOKIE456"})
			fmt.Fprint(w, completeCookieResponse)
		})

		c := newTestClient(ts, "Basic")
		defer c.Close()

		if err := c.InitAuth(); err != nil {
			t.Fatalf("InitAuth: %v", err)
		}
		if err := c.PasswordAuth(); err != nil {
			t.Fatalf("PasswordAuth: %v", err)
		}

		if c.WebVpnCookie != "COOKIE456" {
			t.Errorf("WebVpnCookie = %q, ожидалось COOKIE456", c.WebVpnCookie)
		}
		if c.Cookie() != "COOKIE456" {
			t.Errorf("Cookie() = %q, ожидалось COOKIE456", c.Cookie())
		}
		if c.SessionToken != "COOKIE456" {
			t.Errorf("SessionToken = %q, ожидалось COOKIE456 (cookie имеет приоритет)", c.SessionToken)
		}
	})
}

// Два клиента с разным состоянием — то, ради чего делался форк.
func TestTwoClientsIndependentState(t *testing.T) {
	ts1, _, _ := newMockASA(t, func(w http.ResponseWriter) {
		fmt.Fprint(w, completeTokenResponse)
	})
	ts2, _, _ := newMockASA(t, func(w http.ResponseWriter) {
		http.SetCookie(w, &http.Cookie{Name: "webvpn", Value: "COOKIE456"})
		fmt.Fprint(w, completeCookieResponse)
	})

	c1 := newTestClient(ts1, "Basic")
	c2 := newTestClient(ts2, "Partners")
	defer c1.Close()
	defer c2.Close()

	if err := c1.InitAuth(); err != nil {
		t.Fatalf("c1.InitAuth: %v", err)
	}
	if err := c2.InitAuth(); err != nil {
		t.Fatalf("c2.InitAuth: %v", err)
	}
	if err := c1.PasswordAuth(); err != nil {
		t.Fatalf("c1.PasswordAuth: %v", err)
	}
	if err := c2.PasswordAuth(); err != nil {
		t.Fatalf("c2.PasswordAuth: %v", err)
	}

	if c1.Cookie() != "TOKEN123" {
		t.Errorf("c1.Cookie() = %q, ожидалось TOKEN123", c1.Cookie())
	}
	if c2.Cookie() != "COOKIE456" {
		t.Errorf("c2.Cookie() = %q, ожидалось COOKIE456", c2.Cookie())
	}
	if c1.Prof.Group != "Basic" || c2.Prof.Group != "Partners" {
		t.Error("профили клиентов перемешались")
	}
}

func TestSessionClose(t *testing.T) {
	s := NewSession()
	if s.ActiveClose {
		t.Error("новая сессия не должна быть ActiveClose")
	}
	s.Close() // CSess == nil — не должно паниковать
	if !s.ActiveClose {
		t.Error("после Close() ActiveClose должен быть true")
	}
}
