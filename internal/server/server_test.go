package server

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

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
func (stubRuntime) ImportSession(domain.AccountSessionImport) (domain.Account, error) {
	return domain.Account{}, nil
}
func (stubRuntime) DeleteAccount(string) error   { return nil }
func (stubRuntime) IdentifyAccount(string) error { return nil }

func (stubRuntime) Connect(string) error                                      { return nil }
func (stubRuntime) Disconnect(string) error                                   { return nil }
func (stubRuntime) SubmitCode(string, string) error                           { return nil }
func (stubRuntime) SubmitPassword(string, string) error                       { return nil }
func (stubRuntime) Approve(string) error                                      { return nil }
func (stubRuntime) SendManual(string, string, domain.ManualDestination) error { return nil }
func (stubRuntime) ListPeers(string) ([]domain.PeerRef, error)                { return []domain.PeerRef{}, nil }

type importRuntime struct {
	stubRuntime
	input domain.AccountSessionImport
}

func (r *importRuntime) ImportSession(input domain.AccountSessionImport) (domain.Account, error) {
	r.input = input
	return domain.Account{ID: "imported", Platform: input.Platform, Name: input.Name, Status: "disconnected"}, nil
}

func TestImportAccountSessionMultipart(t *testing.T) {
	t.Parallel()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("platform", connector.Telegram); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("name", " imported account "); err != nil {
		t.Fatal(err)
	}
	file, err := writer.CreateFormFile("file", "protocol.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("fixture-zip")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	runtime := &importRuntime{}
	server := New(db, nil, runtime, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, slog.Default())
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/accounts/import-session", &body)
	request.Host = "127.0.0.1:8765"
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if runtime.input.Platform != connector.Telegram || runtime.input.Name != "imported account" || runtime.input.Filename != "protocol.zip" || !bytes.Equal(runtime.input.Data, []byte("fixture-zip")) {
		t.Fatalf("import input = %#v", runtime.input)
	}
}

func TestImportAccountSessionRejectsOversizedRequest(t *testing.T) {
	t.Parallel()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "protocol.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(bytes.Repeat([]byte{1}, maxSessionImportSize+(128<<10))); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	runtime := &importRuntime{}
	server := New(nil, nil, runtime, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, slog.Default())
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/accounts/import-session", &body)
	request.Host = "127.0.0.1:8765"
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if runtime.input.Data != nil {
		t.Fatal("runtime received oversized session data")
	}
}

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

func TestValidateRouteRequiresNumericPeerIDs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		route domain.Route
		want  string
	}{
		{
			name:  "missing source ID",
			route: domain.Route{AccountID: "account", Name: "route", Sources: []domain.PeerRef{{}}, Targets: []domain.PeerRef{{ChatID: -1002}}},
			want:  "来源 Chat ID",
		},
		{
			name:  "missing target ID",
			route: domain.Route{AccountID: "account", Name: "route", Sources: []domain.PeerRef{{ChatID: -1001}}, Targets: []domain.PeerRef{{}}},
			want:  "目标 Chat ID",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateRoute(&test.route)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateRoute() error = %v, want %q", err, test.want)
			}
		})
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

func TestOutboxMaintenanceAPI(t *testing.T) {
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
	if err := db.EnqueueOutbox([]domain.OutboxJob{{
		RouteID: "route", Target: domain.PeerRef{ChatID: -1}, Text: "test", OrderKey: "-1:0", DedupeKey: "test", SenderAccountIDs: []string{"account"},
	}}); err != nil {
		t.Fatal(err)
	}
	job, err := db.ClaimOutbox(domain.PlatformTelegram, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeferOutbox(job.ID, domain.OutboxReasonDailyLimit, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	s := New(db, secure, stubRuntime{}, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, slog.Default())

	releaseRequest := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/outbox/release-limits", strings.NewReader(`{}`))
	releaseRequest.Host = "127.0.0.1:8765"
	releaseResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(releaseResponse, releaseRequest)
	if releaseResponse.Code != http.StatusOK || !strings.Contains(releaseResponse.Body.String(), `"count":1`) {
		t.Fatalf("release response = %d %s", releaseResponse.Code, releaseResponse.Body.String())
	}

	cancelRequest := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/outbox/cancel-pending", strings.NewReader(`{}`))
	cancelRequest.Host = "127.0.0.1:8765"
	cancelResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(cancelResponse, cancelRequest)
	if cancelResponse.Code != http.StatusOK || !strings.Contains(cancelResponse.Body.String(), `"count":1`) {
		t.Fatalf("cancel response = %d %s", cancelResponse.Code, cancelResponse.Body.String())
	}
}
