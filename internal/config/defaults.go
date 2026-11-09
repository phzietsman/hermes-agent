package config

// Defaults is the built-in configuration. Each task that reads a key registers its default here,
// so a key never has two defaults. It returns a fresh map on every call.
func Defaults() map[string]any {
	return map[string]any{
		"model":              "",
		"fallback_providers": []any{},
		"toolsets":           []any{"hermes-cli"},
	}
}
