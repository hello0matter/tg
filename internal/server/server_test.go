package server

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"tgworkbench/internal/connector"
	"tgworkbench/internal/domain"
	"tgworkbench/internal/store"
	"tgworkbench/internal/vault"
)

type stubRuntime struct{}

func (stubRuntime) Descriptors() []connector.Descriptor {
	return []connector.Descriptor{{Platform: connector.Telegram, Available: true}}
}
func (stubRuntime) CreateAccount(domain.AccountInput) (domain.Account, error) {
	return domain.Account{}, nil
}

func (stubRuntime) Connect(string) error                       { return nil }
func (stubRuntime) Disconnect(string) error                    { return nil }
func (stubRuntime) SubmitCode(string, string) error            { return nil }
func (stubRuntime) SubmitPassword(string, string) error        { return nil }
func (stubRuntime) Approve(string) error                       { return nil }
func (stubRuntime) SendManual(string, string) error            { return nil }
func (stubRuntime) ListPeers(string) ([]domain.PeerRef, error) { return []domain.PeerRef{}, nil }

func TestLocalHostOnly(t *testing.T) {
	t.Parallel()
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}
	server := New(nil, nil, stubRuntime{}, fs.FS(assets), slog.Default())

	local := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/health", nil)
	local.Host = "127.0.0.1:8765"
	localResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(localResponse, local)
	if localResponse.Code != http.StatusOK {
		t.Fatalf("local request status = %d", localResponse.Code)
	}
	index := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	index.Host = "localhost:8765"
	indexResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(indexResponse, index)
	if indexResponse.Code != http.StatusOK || indexResponse.Body.String() != "ok" {
		t.Fatalf("index response = %d %q", indexResponse.Code, indexResponse.Body.String())
	}

	remote := httptest.NewRequest(http.MethodGet, "http://example.com/api/health", nil)
	remote.Host = "evil.example:8765"
	remoteResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(remoteResponse, remote)
	if remoteResponse.Code != http.StatusForbidden {
		t.Fatalf("remote request status = %d", remoteResponse.Code)
	}
}

func TestValidateLocalAddress(t *testing.T) {
	t.Parallel()
	if err := validateLocalAddress("127.0.0.1:8765"); err != nil {
		t.Fatal(err)
	}
	if err := validateLocalAddress("0.0.0.0:8765"); err == nil {
		t.Fatal("public listen address should be rejected")
	}
}

func TestTelegramAPIHashIsEncryptedPreservedAndNotReturned(t *testing.T) {
	dataDir := t.TempDir()
	db, err := store.Open(filepath.Join(dataDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	secure, err := vault.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, secure, stubRuntime{}, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, slog.Default())
	settings, err := db.Settings()
	if err != nil {
		t.Fatal(err)
	}
	settings.Telegram = domain.TelegramSettings{APIID: 123456, APIHash: "secret-api-hash"}
	saved, err := s.persistSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Telegram.APIHash != "" || !saved.Telegram.HasAPIHash {
		t.Fatalf("unsafe settings response: %#v", saved.Telegram)
	}
	encrypted, err := db.Secret(telegramAPIHashSecret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("secret-api-hash")) {
		t.Fatal("Telegram API Hash was stored as plaintext")
	}
	settings.Telegram.APIHash = ""
	if _, err := s.persistSettings(settings); err != nil {
		t.Fatal(err)
	}
	preserved, err := db.Secret(telegramAPIHashSecret)
	if err != nil || !bytes.Equal(encrypted, preserved) {
		t.Fatalf("blank API Hash did not preserve secret: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/settings", nil)
	request.Host = "127.0.0.1:8765"
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "secret-api-hash") {
		t.Fatalf("settings response = %d %s", response.Code, response.Body.String())
	}
	var returned domain.Settings
	if err := json.NewDecoder(response.Body).Decode(&returned); err != nil {
		t.Fatal(err)
	}
	if returned.Telegram.APIHash != "" || !returned.Telegram.HasAPIHash || returned.Telegram.APIID != 123456 {
		t.Fatalf("returned Telegram settings = %#v", returned.Telegram)
	}
}
