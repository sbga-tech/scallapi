package config

import "github.com/router-for-me/CLIProxyAPI/v8/internal/routes"

// NormalizeRoutes validates endpoint route definitions before publishing a config.
func (c *Config) NormalizeRoutes() error {
	if c == nil {
		return nil
	}
	return routes.Normalize(c.Routes)
}
