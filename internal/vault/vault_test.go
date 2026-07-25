package vault

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestVaultPersistsProtectedKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := first.Encrypt("secret-api-hash")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("secret-api-hash")) {
		t.Fatal("vault key file contains plaintext secret")
	}
	second, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := second.Decrypt(ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "secret-api-hash" {
		t.Fatalf("unexpected plaintext %q", plaintext)
	}
}
