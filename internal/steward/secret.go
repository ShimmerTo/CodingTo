package steward

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// secretCipher is the platform-specific encryption hook (Windows DPAPI or
// local AES-GCM fallback).
type secretCipher interface {
	protect(plain []byte) ([]byte, error)
	unprotect(protected []byte) ([]byte, error)
}

// SecretStore persists IM bot credentials per channel. Blobs are encrypted
// with the platform mechanism (Windows DPAPI) or a local AES-GCM fallback
// (macOS/Linux, 0600 key file). Secrets never appear in config.json or logs.
type SecretStore struct {
	dir    string
	cipher secretCipher
}

func NewSecretStore(baseDir string) (*SecretStore, error) {
	dir := filepath.Join(baseDir, "steward", "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	cipher, err := newSecretCipher(dir)
	if err != nil {
		return nil, err
	}
	return &SecretStore{dir: dir, cipher: cipher}, nil
}

func (s *SecretStore) channelPath(channelID int64) string {
	return filepath.Join(s.dir, "c"+strconv.FormatInt(channelID, 10)+".bin")
}

// Save encrypts and writes the secret map for one channel.
func (s *SecretStore) Save(channelID int64, secrets map[string]string) error {
	if len(secrets) == 0 {
		return s.Delete(channelID)
	}
	plain, err := json.Marshal(secrets)
	if err != nil {
		return err
	}
	protected, err := s.cipher.protect(plain)
	if err != nil {
		return err
	}
	path := s.channelPath(channelID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, protected, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Merge preserves stored credentials when an edit form omits masked secret
// fields. Only non-empty updates replace existing values; deleting an entire
// channel still uses Delete explicitly.
func (s *SecretStore) Merge(channelID int64, updates map[string]string) error {
	secrets, err := s.Load(channelID)
	if err != nil {
		return err
	}
	for key, value := range updates {
		if value = strings.TrimSpace(value); value != "" {
			secrets[key] = value
		}
	}
	return s.Save(channelID, secrets)
}

// Load decrypts and returns the secret map for one channel. A missing blob
// returns an empty map, not an error.
func (s *SecretStore) Load(channelID int64) (map[string]string, error) {
	path := s.channelPath(channelID)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := s.cipher.unprotect(raw)
	if err != nil {
		return nil, err
	}
	var secrets map[string]string
	if err := json.Unmarshal(plain, &secrets); err != nil {
		return nil, err
	}
	return secrets, nil
}

// Delete removes the secret blob for one channel.
func (s *SecretStore) Delete(channelID int64) error {
	path := s.channelPath(channelID)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
