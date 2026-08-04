package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"tgworkbench/internal/connector"
	"tgworkbench/internal/domain"
	"tgworkbench/internal/store"
	"tgworkbench/internal/vault"
)

func newCredentialTestManager(t *testing.T) (*Manager, *store.Store, *vault.Vault) {
	t.Helper()
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
	manager := NewManager(dataDir, db, secure, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(manager.queueCancel)
	return manager, db, secure
}

func saveGlobalTelegramCredentials(t *testing.T, db *store.Store, secure *vault.Vault, apiID int, apiHash string) {
	t.Helper()
	settings, err := db.Settings()
	if err != nil {
		t.Fatal(err)
	}
	settings.Telegram.APIID = apiID
	if err := db.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	encrypted, err := secure.Encrypt(apiHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSecret(telegramAPIHashSecret, encrypted); err != nil {
		t.Fatal(err)
	}
}

func TestCreateAccountUsesGlobalTelegramCredentials(t *testing.T) {
	manager, db, secure := newCredentialTestManager(t)
	if _, err := manager.CreateAccount(domain.AccountInput{Name: "global", ConnectorConfig: map[string]string{"phone": "+85212345678"}}); err == nil {
		t.Fatal("account without global or override credentials should fail")
	}
	saveGlobalTelegramCredentials(t, db, secure, 123456, "global-hash")
	account, err := manager.CreateAccount(domain.AccountInput{Name: "global", ConnectorConfig: map[string]string{"phone": "+85212345678"}})
	if err != nil {
		t.Fatal(err)
	}
	stored, encryptedHash, err := db.AccountCredentials(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.APIID != 0 || stored.HasAPIHash || len(encryptedHash) != 0 {
		t.Fatalf("global account stored account override: %#v, %d bytes", stored, len(encryptedHash))
	}
	apiID, apiHash, err := manager.resolveTelegramCredentials(stored, encryptedHash)
	if err != nil || apiID != 123456 || apiHash != "global-hash" {
		t.Fatalf("resolved credentials = %d, %q, %v", apiID, apiHash, err)
	}
}

func TestAccountTelegramCredentialsAreAtomicAndOverrideGlobal(t *testing.T) {
	manager, db, secure := newCredentialTestManager(t)
	saveGlobalTelegramCredentials(t, db, secure, 123456, "global-hash")
	for _, input := range []domain.AccountInput{
		{Name: "id only", ConnectorConfig: map[string]string{"phone": "+85212345678", "apiId": "654321"}},
		{Name: "hash only", ConnectorConfig: map[string]string{"phone": "+85212345678"}, ConnectorSecrets: map[string]string{"apiHash": "account-hash"}},
	} {
		if _, err := manager.CreateAccount(input); err == nil {
			t.Fatalf("incomplete override should fail: %#v", input)
		} else {
			var inputErr connector.InputError
			if !errors.As(err, &inputErr) {
				t.Fatalf("error = %T, want connector.InputError", err)
			}
		}
	}
	account, err := manager.CreateAccount(domain.AccountInput{
		Name: "legacy override", ConnectorConfig: map[string]string{"phone": "+85212345678", "apiId": "654321"}, ConnectorSecrets: map[string]string{"apiHash": "account-hash"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, encryptedHash, err := db.AccountCredentials(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	apiID, apiHash, err := manager.resolveTelegramCredentials(stored, encryptedHash)
	if err != nil || apiID != 654321 || apiHash != "account-hash" {
		t.Fatalf("resolved override = %d, %q, %v", apiID, apiHash, err)
	}
}

func TestDuplicateConnectIsRejectedWithoutStartingAnotherLogin(t *testing.T) {
	manager, _, _ := newCredentialTestManager(t)
	manager.sessions["active"] = &accountSession{}

	err := manager.Connect("active")
	var inputErr connector.InputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("error = %T, want connector.InputError", err)
	}
}

func TestInitialConnectionWatchdogCancelsStalledLogin(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	ready := make(chan struct{})
	done := make(chan struct{})
	go watchInitialTelegramConnection(ctx, ready, done, 10*time.Millisecond, cancel)

	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), errInitialConnectionTimeout) {
			t.Fatalf("cause = %v, want initial connection timeout", context.Cause(ctx))
		}
	case <-time.After(time.Second):
		t.Fatal("watchdog did not cancel stalled login")
	}
}

func TestAuthenticatorReadySignalIsIdempotent(t *testing.T) {
	authenticator := &webAuthenticator{ready: make(chan struct{})}
	authenticator.markReady()
	authenticator.markReady()
	select {
	case <-authenticator.ready:
	default:
		t.Fatal("authenticator did not publish readiness")
	}
}

func TestPeerID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		peer tg.PeerClass
		want int64
	}{
		{peer: &tg.PeerUser{UserID: 42}, want: 42},
		{peer: &tg.PeerChat{ChatID: 42}, want: -42},
		{peer: &tg.PeerChannel{ChannelID: 42}, want: -1_000_000_000_042},
	}
	for _, tc := range cases {
		if got := peerID(tc.peer); got != tc.want {
			t.Fatalf("peerID(%T) = %d, want %d", tc.peer, got, tc.want)
		}
	}
}

func TestFormatButtons(t *testing.T) {
	t.Parallel()
	markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
		&tg.KeyboardButtonURL{Text: "官网", URL: "https://example.com"},
		&tg.KeyboardButtonCallback{Text: "查询", Data: []byte("lookup")},
	}}}}
	if got := formatButtons(markup, "urls_only"); got != "官网 https://example.com" {
		t.Fatalf("urls_only = %q", got)
	}
	if got := formatButtons(markup, "as_text"); got != "官网 https://example.com\n[按钮] 查询" {
		t.Fatalf("as_text = %q", got)
	}
	if got := formatButtons(markup, "drop"); got != "" {
		t.Fatalf("drop = %q", got)
	}
}

func TestFloodWait(t *testing.T) {
	t.Parallel()
	if got := floodWait(errors.New("rpc: FLOOD_WAIT_12")); got != 13*time.Second {
		t.Fatalf("floodWait = %v", got)
	}
}

func TestReusableMedia(t *testing.T) {
	t.Parallel()
	photo := &tg.Photo{ID: 1, AccessHash: 2, FileReference: []byte{3}}
	if _, err := reusableMedia(&tg.MessageMediaPhoto{Photo: photo}, "new caption"); err != nil {
		t.Fatal(err)
	}
	if _, err := reusableMedia(&tg.MessageMediaPoll{}, "poll"); errUnsupportedMedia == nil || err == nil {
		t.Fatal("poll should not be reusable as copied media")
	}
}

func TestSentMessageIDs(t *testing.T) {
	t.Parallel()
	updates := &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateMessageID{ID: 12},
		&tg.UpdateMessageID{ID: 11},
	}}
	ids := sentMessageIDs(updates)
	if len(ids) != 2 || ids[0] != 11 || ids[1] != 12 {
		t.Fatalf("unexpected IDs: %v", ids)
	}
}
