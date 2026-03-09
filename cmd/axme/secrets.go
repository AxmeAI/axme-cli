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
	APIKey     string `json:"api_key,omitempty"`
	ActorToken string `json:"actor_token,omitempty"`
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
	return secrets, nil
}

func (s *keyringSecretStore) Save(contextName string, secrets storedContextSecrets) error {
	if err := setOrDeleteKeyringSecret(contextName, "api_key", secrets.APIKey); err != nil {
		return err
	}
	if err := setOrDeleteKeyringSecret(contextName, "actor_token", secrets.ActorToken); err != nil {
		return err
	}
	return nil
}

type fileSecretStore struct {
	path string
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
	if strings.TrimSpace(secrets.APIKey) == "" && strings.TrimSpace(secrets.ActorToken) == "" {
		delete(allSecrets, contextName)
	} else {
		allSecrets[contextName] = storedContextSecrets{
			APIKey:     strings.TrimSpace(secrets.APIKey),
			ActorToken: strings.TrimSpace(secrets.ActorToken),
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(allSecrets, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0o600)
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
	switch mode {
	case "", "keyring":
		return &keyringSecretStore{}, nil
	case "file":
		return &fileSecretStore{path: filepath.Join(filepath.Dir(cfgFile), "secrets.json")}, nil
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
