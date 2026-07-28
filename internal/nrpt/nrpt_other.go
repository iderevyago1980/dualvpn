//go:build !windows

package nrpt

// NRPT — механизм Windows. На других системах split-DNS решается средствами
// резолвера ОС (systemd-resolved, resolv.conf), поэтому здесь операции
// намеренно ничего не делают: вызывающему коду не нужно ветвиться по ОС.

// Apply на не-Windows не выполняет никаких действий.
func Apply(string, []Rule) error { return nil }

// Remove на не-Windows не выполняет никаких действий.
func Remove(string) error { return nil }
