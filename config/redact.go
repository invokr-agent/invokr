package config

import (
	"fmt"
	"regexp"
)

// envNamePattern matches a plausible environment variable name. A value that
// matches is a real variable reference and must stay readable; anything else
// is presumed to be a pasted credential and gets masked by maskSecret.
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// maskSecret keeps the last 4 characters of a secret for identification;
// shorter secrets are fully masked. Empty stays empty (nothing to protect).
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 4 {
		return "****"
	}
	return "****" + s[len(s)-4:]
}

// String masks the API key so %v/%+v logging of the config can't leak it.
func (c ProviderConfig) String() string {
	return fmt.Sprintf("{base_url:%s model:%s api_key:%s http_timeout:%s stall_timeout:%s max_retries:%d}",
		c.BaseURL, c.Model, maskSecret(c.APIKey), c.HTTPTimeout, c.StallTimeout, c.MaxRetries)
}

// Redacted returns a copy safe for display by diagnostic commands.
func (c AppConfig) Redacted() AppConfig {
	c.Provider.APIKey = maskSecret(c.Provider.APIKey)
	// Provider definitions may each carry a key, so masking the top-level one
	// is not enough once a router table is configured.
	if len(c.Provider.Router.Providers) > 0 {
		masked := make(map[string]RouterProviderConfig, len(c.Provider.Router.Providers))
		for id, provider := range c.Provider.Router.Providers {
			provider.APIKey = maskSecret(provider.APIKey)
			// APIKeyEnv is meant to hold a variable name, but nothing enforces it.
			// Leave a valid env var name readable; mask a pasted credential.
			if !envNamePattern.MatchString(provider.APIKeyEnv) {
				provider.APIKeyEnv = maskSecret(provider.APIKeyEnv)
			}
			masked[id] = provider
		}
		c.Provider.Router.Providers = masked
	}
	return c
}
