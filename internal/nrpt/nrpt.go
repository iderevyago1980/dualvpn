// Package nrpt — правила разрешения имён Windows (Name Resolution Policy
// Table) для TUN-режима.
//
// При двух одновременных туннелях простого назначения DNS-серверов
// интерфейсам недостаточно: Windows опрашивает серверы по метрике
// интерфейса, поэтому зоны одной корпоративной сети уходят в DNS другой и
// не разрешаются. NRPT задаёт соответствие явно: зона → её DNS-серверы,
// независимо от маршрутов и метрик. Это тот же механизм, которым
// пользуется официальный клиент AnyConnect.
//
// Правила требуют прав администратора и снимаются при отключении туннеля.
package nrpt

import "strings"

// commentPrefix помечает правила, созданные приложением: по нему они
// находятся при удалении, чтобы не задеть чужие политики.
const commentPrefix = "DualVPN:"

// Rule — правило для одной зоны.
type Rule struct {
	Namespace string   // ".corp.example" — зона и все её поддомены
	Servers   []string // DNS-серверы, обслуживающие зону
}

// BuildRules превращает список зон split-DNS и адресов DNS-серверов
// в правила NRPT. Обратные зоны (in-addr.arpa) сохраняются: без них не
// работает обратное разрешение адресов внутренней сети.
func BuildRules(zones, servers []string) []Rule {
	cleanServers := make([]string, 0, len(servers))
	for _, s := range servers {
		if s = strings.TrimSpace(s); s != "" {
			cleanServers = append(cleanServers, s)
		}
	}
	if len(cleanServers) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(zones))
	rules := make([]Rule, 0, len(zones))
	for _, z := range zones {
		z = strings.ToLower(strings.Trim(strings.TrimSpace(z), "."))
		if z == "" {
			continue
		}
		if _, dup := seen[z]; dup {
			continue
		}
		seen[z] = struct{}{}
		// Ведущая точка — признак «зона и все поддомены» в терминах NRPT.
		rules = append(rules, Rule{Namespace: "." + z, Servers: cleanServers})
	}
	return rules
}

// comment возвращает метку правил туннеля.
func comment(tunnelID string) string { return commentPrefix + tunnelID }
