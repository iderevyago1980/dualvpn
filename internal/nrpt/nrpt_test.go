package nrpt

import "testing"

func TestBuildRules(t *testing.T) {
	rules := BuildRules(
		[]string{"corp.example", ".intranet.example", "CORP.EXAMPLE", "", "0.10.in-addr.arpa"},
		[]string{"10.0.0.11", " ", "10.0.0.12"},
	)

	if len(rules) != 3 {
		t.Fatalf("ожидалось 3 правила (дубликат и пустая зона отброшены), получено %d: %+v", len(rules), rules)
	}

	// Ведущая точка означает «зона и все поддомены».
	want := map[string]bool{".corp.example": true, ".intranet.example": true, ".0.10.in-addr.arpa": true}
	for _, r := range rules {
		if !want[r.Namespace] {
			t.Errorf("неожиданная зона %q", r.Namespace)
		}
		if len(r.Servers) != 2 || r.Servers[0] != "10.0.0.11" || r.Servers[1] != "10.0.0.12" {
			t.Errorf("серверы правила %q = %v", r.Namespace, r.Servers)
		}
	}
}

// Обратные зоны нужны для разрешения адресов внутренней сети в имена —
// в отличие от PAC, здесь их отбрасывать нельзя.
func TestBuildRulesKeepsReverseZones(t *testing.T) {
	rules := BuildRules([]string{"1.168.192.in-addr.arpa"}, []string{"10.0.0.11"})
	if len(rules) != 1 || rules[0].Namespace != ".1.168.192.in-addr.arpa" {
		t.Errorf("обратная зона потеряна: %+v", rules)
	}
}

// Без адресов DNS-серверов правило бессмысленно: оно направило бы зону
// в никуда и сломало бы её разрешение вовсе.
func TestBuildRulesWithoutServers(t *testing.T) {
	if rules := BuildRules([]string{"corp.example"}, nil); len(rules) != 0 {
		t.Errorf("без серверов правил быть не должно: %+v", rules)
	}
	if rules := BuildRules([]string{"corp.example"}, []string{"", "  "}); len(rules) != 0 {
		t.Errorf("пустые адреса серверов не считаются: %+v", rules)
	}
}
