package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/session"
	_ "modernc.org/sqlite"

	"tgworkbench/internal/domain"
)

func makeTelethonSQLite(t *testing.T, authKey []byte) []byte {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "fixture.session")
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE sessions (dc_id INTEGER PRIMARY KEY, server_address TEXT, port INTEGER, auth_key BLOB, takeout_id INTEGER);
INSERT INTO sessions(dc_id, server_address, port, auth_key, takeout_id) VALUES(2, '149.154.167.40', 443, ?, NULL)`, authKey); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func makeProtocolZIP(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func validProtocolZIP(t *testing.T, authKey []byte) []byte {
	t.Helper()
	metadata, err := json.Marshal(map[string]any{
		"phone": "+10000000000", "app_id": 123456, "app_hash": "fixture-api-hash",
	})
	if err != nil {
		t.Fatal(err)
	}
	return makeProtocolZIP(t, map[string][]byte{
		"account.session": makeTelethonSQLite(t, authKey),
		"account.json":    metadata,
	})
}

func TestImportTelethonSessionStoresEncryptedGotdSession(t *testing.T) {
	manager, db, secure := newCredentialTestManager(t)
	authKey := bytes.Repeat([]byte{0x42}, 256)
	account, err := manager.ImportSession(domain.AccountSessionImport{
		Platform: "telegram", Name: "imported", Filename: "protocol.zip", Data: validProtocolZIP(t, authKey),
	})
	if err != nil {
		t.Fatal(err)
	}
	if account.Status != "disconnected" || account.APIID != 123456 || !account.HasAPIHash {
		t.Fatalf("imported account = %#v", account)
	}
	stored, encryptedHash, err := db.AccountCredentials(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	apiID, apiHash, err := manager.resolveTelegramCredentials(stored, encryptedHash)
	if err != nil || apiID != 123456 || apiHash != "fixture-api-hash" {
		t.Fatalf("credentials = %d, %q, %v", apiID, apiHash, err)
	}
	storage := &encryptedSessionStorage{path: filepath.Join(manager.dataDir, "sessions", account.ID+".session"), vault: secure}
	loaded, err := (&session.Loader{Storage: storage}).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DC != 2 || loaded.Addr != "149.154.167.40:443" || !bytes.Equal(loaded.AuthKey, authKey) || len(loaded.AuthKeyID) != 8 {
		t.Fatalf("loaded session metadata invalid: DC=%d Addr=%q key=%d id=%d", loaded.DC, loaded.Addr, len(loaded.AuthKey), len(loaded.AuthKeyID))
	}
	onDisk, err := os.ReadFile(storage.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(onDisk, authKey) || bytes.Contains(onDisk, []byte("fixture-api-hash")) {
		t.Fatal("session or API credentials were stored in plaintext")
	}
	if err := manager.DeleteAccount(account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(storage.path); !os.IsNotExist(err) {
		t.Fatalf("encrypted session still exists after account deletion: %v", err)
	}
	if _, _, err := db.AccountCredentials(account.ID); err == nil {
		t.Fatal("account record still exists after deletion")
	}
}

func TestParseTelethonZIPRejectsUnsafePackages(t *testing.T) {
	validSession := makeTelethonSQLite(t, bytes.Repeat([]byte{1}, 256))
	validJSON := []byte(`{"phone":"+10000000000","app_id":123456,"app_hash":"fixture-api-hash"}`)
	tests := []struct {
		name    string
		entries map[string][]byte
		want    string
	}{
		{name: "path traversal", entries: map[string][]byte{"../account.session": validSession, "account.json": validJSON}, want: "不安全路径"},
		{name: "multiple sessions", entries: map[string][]byte{"one.session": validSession, "two.session": validSession, "account.json": validJSON}, want: "一个 .session"},
		{name: "missing metadata", entries: map[string][]byte{"account.session": validSession}, want: "必须包含"},
		{name: "unexpected executable", entries: map[string][]byte{"account.session": validSession, "account.json": validJSON, "run.exe": {1}}, want: "不允许的文件类型"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseTelethonZIP(makeProtocolZIP(t, test.entries), t.TempDir())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestParseTelethonZIPRejectsInvalidAuthKey(t *testing.T) {
	_, err := parseTelethonZIP(validProtocolZIP(t, bytes.Repeat([]byte{1}, 32)), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "授权密钥无效") {
		t.Fatalf("error = %v", err)
	}
}
