package sslcon

import (
	"strings"
	"testing"
	"text/template"
)

// execTpl рендерит шаблон с данными так же, как это делает tplPost.
func execTpl(t *testing.T, tmpl string, data any) string {
	t.Helper()
	tp, err := template.New("t").Parse(tmpl)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var b strings.Builder
	if err := tp.Execute(&b, data); err != nil {
		t.Fatalf("exec: %v", err)
	}
	return b.String()
}

// TestAuthReplyGroupSelectGated — <group-select> уходит в auth-reply только
// когда сервер предлагал выбор группы (SendGroupSelect=true). ocserv без
// select-group групп не предлагает → group-select слать нельзя (иначе 401).
func TestAuthReplyGroupSelectGated(t *testing.T) {
	on := execTpl(t, templateAuthReply, &Profile{Group: "LAB", SendGroupSelect: true})
	if !strings.Contains(on, "<group-select>LAB</group-select>") {
		t.Errorf("SendGroupSelect=true: ожидался <group-select>LAB</group-select>, тело:\n%s", on)
	}
	off := execTpl(t, templateAuthReply, &Profile{Group: "LAB", SendGroupSelect: false})
	if strings.Contains(off, "<group-select") {
		t.Errorf("SendGroupSelect=false: <group-select> не должен присутствовать, тело:\n%s", off)
	}
}

// TestTwoFAReplyGroupSelectGated — тот же гейт в шаблоне ответа на 2FA.
func TestTwoFAReplyGroupSelectGated(t *testing.T) {
	// Группа выбрана на шаге первичного логина. Повторный <group-select> в
	// ответе на challenge реальная ASA трактует как новую попытку логина и
	// отвечает «Login failed.» даже на верный код — поэтому его не должно
	// быть ни при каком значении SendGroupSelect.
	for _, send := range []bool{true, false} {
		body := execTpl(t, template2FAReply, twoFAData(&Profile{Group: "LAB", SendGroupSelect: send}))
		if strings.Contains(body, "<group-select") {
			t.Errorf("SendGroupSelect=%v: <group-select> не должен уходить в ответе на 2FA, тело:\n%s", send, body)
		}
	}
}

// twoFAData собирает данные шаблона ответа на 2FA — тот же набор полей,
// что подставляет tplPost.
func twoFAData(p *Profile) any {
	return struct {
		*Profile
		Code         string
		CodeField    string
		OpaqueXML    string
		HasOpaque    bool
		SendUsername bool
	}{p, "123456", codeElement("answer"), "", false, false}
}

// Поле формы answer соответствует элементу <password> (так же поступает
// OpenConnect); остальные имена уходят как есть.
func TestCodeElementMapping(t *testing.T) {
	cases := map[string]string{
		"answer":             "password",
		"whichpin":           "password",
		"new_password":       "password",
		"":                   "password",
		"secondary_password": "secondary_password",
	}
	for field, want := range cases {
		if got := codeElement(field); got != want {
			t.Errorf("codeElement(%q) = %q, ожидалось %q", field, got, want)
		}
	}
}

// В ответе на challenge повторяются только поля формы. <username> в ней нет,
// и его отправка заставляет ASA считать это новой попыткой первичного логина.
func TestTwoFAReplyOmitsUsernameByDefault(t *testing.T) {
	body := execTpl(t, template2FAReply, twoFAData(&Profile{Username: "u"}))
	if !strings.Contains(body, "<password>123456</password>") {
		t.Errorf("код должен уходить в <password>, тело:\n%s", body)
	}
	if strings.Contains(body, "<username>") {
		t.Errorf("<username> не должен уходить в ответе на challenge, тело:\n%s", body)
	}
}

// Если форма всё же содержит username — отправляем.
func TestTwoFAReplyKeepsUsernameWhenFormAsks(t *testing.T) {
	data := struct {
		*Profile
		Code         string
		CodeField    string
		OpaqueXML    string
		HasOpaque    bool
		SendUsername bool
	}{&Profile{Username: "u"}, "123456", "password", "", false, true}

	body := execTpl(t, template2FAReply, data)
	if !strings.Contains(body, "<username>u</username>") {
		t.Errorf("<username> должен уходить, когда форма его содержит, тело:\n%s", body)
	}
}

// <opaque> из challenge-ответа возвращается дословно: в нём auth-handle,
// которым ASA связывает ответ с выданным challenge.
func TestTwoFAReplyEchoesChallengeOpaque(t *testing.T) {
	data := struct {
		*Profile
		Code         string
		CodeField    string
		OpaqueXML    string
		HasOpaque    bool
		SendUsername bool
	}{&Profile{TunnelGroup: "СТАРАЯ"}, "123456", "password",
		"<tunnel-group>TG</tunnel-group><auth-handle>-42</auth-handle>", true, false}

	body := execTpl(t, template2FAReply, data)
	if !strings.Contains(body, "<auth-handle>-42</auth-handle>") {
		t.Errorf("auth-handle из challenge потерян, тело:\n%s", body)
	}
	if strings.Contains(body, "СТАРАЯ") {
		t.Errorf("вместо opaque из challenge подставлен профиль, тело:\n%s", body)
	}
}
