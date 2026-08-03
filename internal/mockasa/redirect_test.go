package mockasa_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dualvpn/internal/mockasa"
	"dualvpn/internal/vpn/sslcon"
)

// newRedirector поднимает TLS-сервер, который на любой запрос отвечает
// перенаправлением на target. Так ведёт себя vpn.example.com: отдаёт
// 302 с Location на настоящий адрес шлюза.
func newRedirector(t *testing.T, status int, location string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", location)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newASA поднимает мок-шлюз со списком групп.
func newASA(t *testing.T) *mockasa.Server {
	t.Helper()
	srv, err := mockasa.New(mockasa.Config{
		Groups:     []string{"Основная", "Резервная"},
		Users:      map[string]string{"user": "pass"},
		VPNAddress: "10.10.0.5",
		HostIP:     "10.10.0.1",
	})
	if err != nil {
		t.Fatalf("запуск шлюза: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

// TestFetchGroupsFollowsRedirect — клиент обязан идти за перенаправлением:
// шлюз отвечает с одного имени, а обслуживает подключения на другом.
// Без этого пользователь получал «auth error 302 Temporary moved» и не
// понимал, какой адрес указывать.
func TestFetchGroupsFollowsRedirect(t *testing.T) {
	asa := newASA(t)

	for _, status := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			front := newRedirector(t, status, "https://"+asa.Addr()+"/")
			host := strings.TrimPrefix(front.URL, "https://")

			groups, err := sslcon.FetchGroups(host, true)
			if err != nil {
				t.Fatalf("FetchGroups через перенаправление: %v", err)
			}
			if len(groups) != 2 || groups[0] != "Основная" {
				t.Errorf("группы = %v, ожидались группы целевого шлюза", groups)
			}
		})
	}
}

// TestRedirectLoopStops — перенаправление на самого себя не должно
// зацикливать подключение.
func TestRedirectLoopStops(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", srv.URL+"/")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	_, err := sslcon.FetchGroups(strings.TrimPrefix(srv.URL, "https://"), true)
	if err == nil {
		t.Fatal("перенаправление на себя не остановлено")
	}
	if !strings.Contains(err.Error(), "сам на себя") {
		t.Errorf("непонятная ошибка зацикливания: %v", err)
	}
}

// TestRedirectToPortalIsExplained — перенаправление на путь того же сервера
// (веб-портал вместо VPN-эндпоинта) должно объясняться человеку, а не
// возвращать голый код ответа.
func TestRedirectToPortalIsExplained(t *testing.T) {
	front := newRedirector(t, http.StatusFound, "/+CSCOE+/logon.html")

	_, err := sslcon.FetchGroups(strings.TrimPrefix(front.URL, "https://"), true)
	if err == nil {
		t.Fatal("перенаправление на страницу портала принято за успех")
	}
	if !strings.Contains(err.Error(), "портал") {
		t.Errorf("ошибка не объясняет причину: %v", err)
	}
}

// TestRedirectToHTTPRejected — понижение до http означало бы отправку
// учётных данных открытым текстом.
func TestRedirectToHTTPRejected(t *testing.T) {
	front := newRedirector(t, http.StatusFound, "http://example.com/")

	_, err := sslcon.FetchGroups(strings.TrimPrefix(front.URL, "https://"), true)
	if err == nil {
		t.Fatal("перенаправление на http принято")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("ошибка не объясняет причину: %v", err)
	}
}
