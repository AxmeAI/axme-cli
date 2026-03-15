package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	keyring "github.com/zalando/go-keyring"
)

const (
	axmeCLISecretStorageEnv = "AXME_CLI_SECRET_STORAGE"
	axmeCLIKeyringService   = "axme-cli"
)

type storedContextSecrets struct {
	APIKey       string `json:"api_key,omitempty"`
	ActorToken   string `json:"actor_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

type secretStore interface {
	Mode() string
	Detail() string
	Load(contextName string) (storedContextSecrets, error)
	Save(contextName string, secrets storedContextSecrets) error
}

type keyringSecretStore struct{}

func (s *keyringSecretStore) Mode() string {
	return "keyring"
}

func (s *keyringSecretStore) Detail() string {
	return "OS-native secure storage"
}

func (s *keyringSecretStore) Load(contextName string) (storedContextSecrets, error) {
	secrets := storedContextSecrets{}
	apiKey, err := keyring.Get(axmeCLIKeyringService, secretStoreUsername(contextName, "api_key"))
	if err == nil {
		secrets.APIKey = apiKey
	} else if !errors.Is(err, keyring.ErrNotFound) {
		return storedContextSecrets{}, keyringUnavailableError(err)
	}
	actorToken, err := keyring.Get(axmeCLIKeyringService, secretStoreUsername(contextName, "actor_token"))
	if err == nil {
		secrets.ActorToken = actorToken
	} else if !errors.Is(err, keyring.ErrNotFound) {
		return storedContextSecrets{}, keyringUnavailableError(err)
	}
	refreshToken, err := keyring.Get(axmeCLIKeyringService, secretStoreUsername(contextName, "refresh_token"))
	if err == nil {
		secrets.RefreshToken = refreshToken
	} else if !errors.Is(err, keyring.ErrNotFound) {
		return storedContextSecrets{}, keyringUnavailableError(err)
	}
	return secrets, nil
}

func (s *keyringSecretStore) Save(contextName string, secrets storedContextSecrets) error {
	if err := setOrDeleteKeyringSecret(contextName, "api_key", secrets.APIKey); err != nil {
		return err
	}
	if err := setOrDeleteKeyringSecret(contextName, "actor_token", secrets.ActorToken); err != nil {
		return err
	}
	if err := setOrDeleteKeyringSecret(contextName, "refresh_token", secrets.RefreshToken); err != nil {
		return err
	}
	return nil
}

type fileSecretStore struct {
	path         string
	autoFallback bool
}

func (s *fileSecretStore) Mode() string {
	return "file"
}

func (s *fileSecretStore) Detail() string {
	return s.path
}

func (s *fileSecretStore) Load(contextName string) (storedContextSecrets, error) {
	allSecrets, err := s.readAll()
	if err != nil {
		return storedContextSecrets{}, err
	}
	return allSecrets[contextName], nil
}

func (s *fileSecretStore) Save(contextName string, secrets storedContextSecrets) error {
	allSecrets, err := s.readAll()
	if err != nil {
		return err
	}
	apiKey := strings.TrimSpace(secrets.APIKey)
	actorToken := strings.TrimSpace(secrets.ActorToken)
	refreshToken := strings.TrimSpace(secrets.RefreshToken)
	// Only delete the context entry when ALL three credentials are absent.
	// Previously the condition only checked APIKey+ActorToken which caused
	// the refresh_token to be silently dropped when called with an expired
	// (empty) actor_token during proactive refresh, breaking the 30-day
	// session and forcing a full email re-login.
	if apiKey == "" && actorToken == "" && refreshToken == "" {
		delete(allSecrets, contextName)
	} else {
		allSecrets[contextName] = storedContextSecrets{
			APIKey:       apiKey,
			ActorToken:   actorToken,
			RefreshToken: refreshToken,
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(allSecrets, "", "  ")
	if err != nil {
		return err
	}
	// Write atomically: temp file → fsync → rename.
	// A direct WriteFile leaves a window where a crash produces a truncated/empty
	// secrets.json, losing the refresh_token and killing the session.
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".secrets-*.tmp")
	if err != nil {
		return fmt.Errorf("secrets save: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("secrets save: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("secrets save: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("secrets save: close temp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("secrets save: chmod: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("secrets save: rename: %w", err)
	}
	return nil
}

func (s *fileSecretStore) readAll() (map[string]storedContextSecrets, error) {
	if !fileExists(s.path) {
		return map[string]storedContextSecrets{}, nil
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	out := map[string]storedContextSecrets{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func initSecretStore(cfgFile string) (secretStore, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(axmeCLISecretStorageEnv)))
	filePath := filepath.Join(filepath.Dir(cfgFile), "secrets.json")
	switch mode {
	case "file":
		return &fileSecretStore{path: filePath}, nil
	case "keyring":
		return &keyringSecretStore{}, nil
	case "":
		// Default: file-based storage. Reliable across SSH, CI/CD, containers,
		// headless servers. Keyring is opt-in via AXME_CLI_SECRET_STORAGE=keyring.
		return &fileSecretStore{path: filePath}, nil
	default:
		return nil, fmt.Errorf(
			"unsupported secret storage mode %q in %s (supported: keyring, file)",
			mode,
			axmeCLISecretStorageEnv,
		)
	}
}

func secretStoreUsername(contextName string, field string) string {
	return fmt.Sprintf("context:%s:%s", contextName, field)
}

func setOrDeleteKeyringSecret(contextName string, field string, value string) error {
	normalized := strings.TrimSpace(value)
	username := secretStoreUsername(contextName, field)
	if normalized == "" {
		err := keyring.Delete(axmeCLIKeyringService, username)
		if err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return keyringUnavailableError(err)
		}
		return nil
	}
	if err := keyring.Set(axmeCLIKeyringService, username, normalized); err != nil {
		return keyringUnavailableError(err)
	}
	return nil
}

func keyringUnavailableError(err error) error {
	return fmt.Errorf(
		"secure secret storage is unavailable: %w. If you are in a headless or CI environment, set %s=file to use explicit file-based fallback",
		err,
		axmeCLISecretStorageEnv,
	)
}
