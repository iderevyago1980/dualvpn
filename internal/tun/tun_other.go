//go:build !linux

package tun

// Create — заглушка для не-Linux платформ.
// Windows: планируется загрузка wintun.dll и создание адаптера через
// Wintun API (WintunCreateAdapter/WintunStartSession) — будет реализовано
// на этапе Windows-сборки.
func Create(cfg Config) (*Device, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return nil, nil
}
