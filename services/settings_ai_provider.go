package services

import (
	"fmt"
)

// getAIProviderConfig returns one provider configuration with its key
// decrypted for trusted backend use. It is deliberately unexported so a
// renderer cannot select or read provider credentials directly.
func (s *SettingsService) getAIProviderConfig(configID string) (AIProviderConfig, error) {
	if s == nil || configID == "" {
		return AIProviderConfig{}, fmt.Errorf("AI provider config ID is required: %w", ErrInvalidInput)
	}
	s.pathMu.RLock()
	defer s.pathMu.RUnlock()
	for _, config := range s.readRawProviderConfigsLocked() {
		if config.ID != configID {
			continue
		}
		if config.APIKey == "" {
			return AIProviderConfig{}, fmt.Errorf("AI provider %q has no API key: %w", configID, ErrNotAllowed)
		}
		key, err := DecryptSecret(keyringAccount, config.APIKey)
		if err != nil {
			return AIProviderConfig{}, fmt.Errorf("decrypt AI provider %q key: %w", configID, err)
		}
		config.APIKey = key
		return config, nil
	}
	return AIProviderConfig{}, fmt.Errorf("AI provider %q was not found: %w", configID, ErrNotFound)
}
