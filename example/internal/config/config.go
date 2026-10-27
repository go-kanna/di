// Package config holds the settings of the shop.
package config

// Config is the settings of the shop.
type Config struct {
	// Store names the store the users are kept in.
	Store string
}

// Load reads the settings, which a real application would take from its
// environment.
func Load() (Config, error) {
	return Config{Store: "users"}, nil
}
