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
	type data struct {
		*Profile
		Code string
	}
	on := execTpl(t, template2FAReply, data{&Profile{Group: "LAB", SendGroupSelect: true}, "123456"})
	if !strings.Contains(on, "<group-select>LAB</group-select>") {
		t.Errorf("2FA SendGroupSelect=true: ожидался <group-select>, тело:\n%s", on)
	}
	off := execTpl(t, template2FAReply, data{&Profile{Group: "LAB", SendGroupSelect: false}, "123456"})
	if strings.Contains(off, "<group-select") {
		t.Errorf("2FA SendGroupSelect=false: <group-select> не должен присутствовать, тело:\n%s", off)
	}
}
