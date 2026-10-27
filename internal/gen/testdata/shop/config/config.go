// Package config loads the settings of the shop.
package config

// Config is the settings of the shop.
type Config struct {
	DSN string
}

// Load reads the settings.
func Load() (Config, error) {
	return Config{DSN: "postgres://localhost/shop"}, nil
}
