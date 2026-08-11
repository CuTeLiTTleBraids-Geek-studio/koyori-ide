package services

import (
	"context"
	"errors"
)

// SettingsDatabaseSecretResolver resolves opaque credential config IDs only
// inside the Go backend. Plaintext DSNs never cross a Wails binding.
type SettingsDatabaseSecretResolver struct {
	settings *SettingsService
}

func NewSettingsDatabaseSecretResolver(settings *SettingsService) *SettingsDatabaseSecretResolver {
	return &SettingsDatabaseSecretResolver{settings: settings}
}

func (r *SettingsDatabaseSecretResolver) ResolveDatabaseSecret(
	_ context.Context,
	configID string,
) (string, error) {
	if r == nil || r.settings == nil {
		return "", errors.New("database secrets service is unavailable")
	}
	return r.settings.getAPIKeyForConfig(configID)
}
