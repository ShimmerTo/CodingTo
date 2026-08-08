//go:build !windows

package steward

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
)

const aesKeyFileName = "key.bin"

// aesCipher encrypts secret blobs with AES-256-GCM under a local 256-bit key
// file (0600). macOS Keychain / Linux keyring integration is not implemented
// yet; this is the documented local-weak fallback.
type aesCipher struct {
	key []byte
}

// newSecretCipher loads or creates the local AES key file in dir.
func newSecretCipher(dir string) (secretCipher, error) {
	path := filepath.Join(dir, aesKeyFileName)
	raw, err := os.ReadFile(path)
	if err == nil && len(raw) == 32 {
		return &aesCipher{key: raw}, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, key, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return &aesCipher{key: key}, nil
}

func (c *aesCipher) protect(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errors.New("secret is empty")
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plain, nil), nil
}

func (c *aesCipher) unprotect(protected []byte) ([]byte, error) {
	if len(protected) == 0 {
		return nil, errors.New("protected secret is empty")
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(protected) < aead.NonceSize() {
		return nil, errors.New("protected secret is truncated")
	}
	nonce, ciphertext := protected[:aead.NonceSize()], protected[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}
	return plain, nil
}
