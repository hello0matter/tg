package connector

import (
	"errors"
	"path/filepath"
	"testing"

	"tgworkbench/internal/domain"
	"tgworkbench/internal/store"
)

type fakeAdapter struct {
	platform  string
	connected string
}

func TestRegistryRejectsUnknownPlatform(t *testing.T) {
	t.Parallel()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	registry := NewRegistry(db)
	_, err = registry.CreateAccount(domain.AccountInput{Platform: "unknown"})
	var inputErr InputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("CreateAccount error = %v, want InputError", err)
	}
}

func (f *fakeAdapter) Descriptor() Descriptor {
	return Descriptor{Platform: f.platform, Name: f.platform, Available: true}
}
func (f *fakeAdapter) CreateAccount(input domain.AccountInput) (domain.Account, error) {
	return domain.Account{ID: "created", Platform: input.Platform, Name: input.Name}, nil
}
func (f *fakeAdapter) ImportSession(input domain.AccountSessionImport) (domain.Account, error) {
	return domain.Account{ID: "imported", Platform: input.Platform, Name: input.Name}, nil
}
func (f *fakeAdapter) DeleteAccount(string) error                                { return nil }
func (f *fakeAdapter) Connect(accountID string) error                            { f.connected = accountID; return nil }
func (f *fakeAdapter) Disconnect(string) error                                   { return nil }
func (f *fakeAdapter) SubmitCode(string, string) error                           { return nil }
func (f *fakeAdapter) SubmitPassword(string, string) error                       { return nil }
func (f *fakeAdapter) Approve(string) error                                      { return nil }
func (f *fakeAdapter) SendManual(string, string, domain.ManualDestination) error { return nil }
func (f *fakeAdapter) ListPeers(string) ([]domain.PeerRef, error)                { return nil, nil }

func TestRegistryDispatchesByAccountPlatform(t *testing.T) {
	t.Parallel()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	account, err := db.SaveAccount(domain.AccountInput{Platform: "webhook", Name: "bot", Phone: "n/a", APIID: 1}, []byte("encrypted"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{platform: "webhook"}
	registry := NewRegistry(db)
	if err := registry.Register(adapter); err != nil {
		t.Fatal(err)
	}
	created, err := registry.CreateAccount(domain.AccountInput{Platform: "webhook", Name: "new"})
	if err != nil || created.Platform != "webhook" {
		t.Fatalf("created account = %#v, %v", created, err)
	}
	if err := registry.Connect(account.ID); err != nil {
		t.Fatal(err)
	}
	if adapter.connected != account.ID {
		t.Fatalf("connected account = %q, want %q", adapter.connected, account.ID)
	}
	if got := registry.Descriptors(); len(got) != 1 || got[0].Platform != "webhook" {
		t.Fatalf("descriptors = %#v", got)
	}
}
