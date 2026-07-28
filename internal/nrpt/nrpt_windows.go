//go:build windows

package nrpt

import (
	"fmt"
	"os/exec"
	"strings"
)

// Apply создаёт правила NRPT для туннеля, предварительно сняв старые с той
// же меткой (повторное подключение не должно плодить дубликаты).
// Ошибки отдельных зон накапливаются: часть правил лучше, чем ни одного.
func Apply(tunnelID string, rules []Rule) error {
	if err := Remove(tunnelID); err != nil {
		return err
	}
	var errs []string
	for _, r := range rules {
		script := fmt.Sprintf(
			`Add-DnsClientNrptRule -Namespace '%s' -NameServers @(%s) -Comment '%s' -ErrorAction Stop`,
			psQuote(r.Namespace), psList(r.Servers), psQuote(comment(tunnelID)))
		if out, err := runPS(script); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v (%s)", r.Namespace, err, out))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("правила NRPT: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Remove снимает все правила, созданные для указанного туннеля.
func Remove(tunnelID string) error {
	script := fmt.Sprintf(
		`Get-DnsClientNrptRule | Where-Object { $_.Comment -eq '%s' } | `+
			`ForEach-Object { Remove-DnsClientNrptRule -Name $_.Name -Force -ErrorAction Stop }`,
		psQuote(comment(tunnelID)))
	if out, err := runPS(script); err != nil {
		return fmt.Errorf("снятие правил NRPT: %w (%s)", err, out)
	}
	return nil
}

// runPS выполняет команду PowerShell. Cmdlet'ы DnsClient доступны только
// через PowerShell — соответствующего консольного аналога у netsh нет.
func runPS(script string) (string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// psQuote экранирует строку для одинарных кавычек PowerShell.
func psQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

// psList собирает массив PowerShell из адресов серверов.
func psList(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, s := range items {
		quoted = append(quoted, "'"+psQuote(s)+"'")
	}
	return strings.Join(quoted, ",")
}
