package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
)

type Vault struct {
	aead cipher.AEAD
}

func Open(dataDir string) (*Vault, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dataDir, "vault.key")
	stored, err := os.ReadFile(keyPath)
	var key []byte
	if os.IsNotExist(err) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		protected, err := protectKey(key)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(keyPath, protected, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if len(stored) == 32 {
		key = stored
		protected, err := protectKey(key)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(keyPath, protected, 0o600); err != nil {
			return nil, err
		}
	} else {
		key, err = unprotectKey(stored)
		if err != nil {
			return nil, fmt.Errorf("unprotect vault key: %w", err)
		}
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid vault key length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{aead: aead}, nil
}

func (v *Vault) Encrypt(plaintext string) ([]byte, error) {
	if plaintext == "" {
		return nil, nil
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return v.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

func (v *Vault) Decrypt(ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	nonceSize := v.aead.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("invalid encrypted value")
	}
	plaintext, err := v.aead.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], nil)
	return string(plaintext), err
}
