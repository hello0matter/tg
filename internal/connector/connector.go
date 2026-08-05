package connector

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"tgworkbench/internal/domain"
	"tgworkbench/internal/store"
)

const Telegram = domain.PlatformTelegram

type InputError struct {
	Message string
}

func (e InputError) Error() string { return e.Message }

type Capabilities struct {
	Text         bool `json:"text"`
	Media        bool `json:"media"`
	Albums       bool `json:"albums"`
	Edits        bool `json:"edits"`
	Deletes      bool `json:"deletes"`
	Threads      bool `json:"threads"`
	URLButtons   bool `json:"urlButtons"`
	Callback     bool `json:"callbackButtons"`
	UserSessions bool `json:"userSessions"`
	BotWebhooks  bool `json:"botWebhooks"`
}

type Descriptor struct {
	Platform                     string            `json:"platform"`
	Name                         string            `json:"name"`
	Available                    bool              `json:"available"`
	DefaultCredentialsConfigured bool              `json:"defaultCredentialsConfigured"`
	Capabilities                 Capabilities      `json:"capabilities"`
	Credentials                  []CredentialField `json:"credentials"`
}

type CredentialField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Kind        string `json:"kind"`
	Secret      bool   `json:"secret"`
	Required    bool   `json:"required"`
	Shared      bool   `json:"shared"`
	Placeholder string `json:"placeholder"`
}

// Adapter is the runtime boundary for a messaging platform. Implementations own
// platform authentication, peer discovery, inbound updates, and outbound delivery.
type Adapter interface {
	Descriptor() Descriptor
	CreateAccount(input domain.AccountInput) (domain.Account, error)
	Connect(accountID string) error
	Disconnect(accountID string) error
	SubmitCode(accountID, code string) error
	SubmitPassword(accountID, password string) error
	Approve(reviewID string) error
	SendManual(routeID, text string, destination domain.ManualDestination) error
	ListPeers(accountID string) ([]domain.PeerRef, error)
}

type Registry struct {
	store *store.Store
	mu    sync.RWMutex
	items map[string]Adapter
}

func NewRegistry(store *store.Store) *Registry {
	return &Registry{store: store, items: make(map[string]Adapter)}
}

func (r *Registry) Register(adapter Adapter) error {
	descriptor := adapter.Descriptor()
	if descriptor.Platform == "" {
		return errors.New("connector platform cannot be empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[descriptor.Platform]; exists {
		return fmt.Errorf("connector %q already registered", descriptor.Platform)
	}
	r.items[descriptor.Platform] = adapter
	return nil
}

func (r *Registry) Descriptors() []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Descriptor, 0, len(r.items))
	for _, adapter := range r.items {
		result = append(result, adapter.Descriptor())
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Platform < result[j].Platform })
	return result
}

func (r *Registry) Connect(accountID string) error {
	adapter, err := r.forAccount(accountID)
	if err != nil {
		return err
	}
	return adapter.Connect(accountID)
}

func (r *Registry) CreateAccount(input domain.AccountInput) (domain.Account, error) {
	platform := input.Platform
	if platform == "" {
		platform = Telegram
		input.Platform = platform
	}
	r.mu.RLock()
	adapter := r.items[platform]
	r.mu.RUnlock()
	if adapter == nil {
		return domain.Account{}, InputError{Message: fmt.Sprintf("connector %q is not installed", platform)}
	}
	return adapter.CreateAccount(input)
}

func (r *Registry) Disconnect(accountID string) error {
	adapter, err := r.forAccount(accountID)
	if err != nil {
		return err
	}
	return adapter.Disconnect(accountID)
}

func (r *Registry) SubmitCode(accountID, code string) error {
	adapter, err := r.forAccount(accountID)
	if err != nil {
		return err
	}
	return adapter.SubmitCode(accountID, code)
}

func (r *Registry) SubmitPassword(accountID, password string) error {
	adapter, err := r.forAccount(accountID)
	if err != nil {
		return err
	}
	return adapter.SubmitPassword(accountID, password)
}

func (r *Registry) Approve(reviewID string) error {
	review, err := r.store.Review(reviewID)
	if err != nil {
		return err
	}
	adapter, err := r.forRoute(review.RouteID)
	if err != nil {
		return err
	}
	return adapter.Approve(reviewID)
}

func (r *Registry) SendManual(routeID, text string, destination domain.ManualDestination) error {
	adapter, err := r.forRoute(routeID)
	if err != nil {
		return err
	}
	return adapter.SendManual(routeID, text, destination)
}

func (r *Registry) ListPeers(accountID string) ([]domain.PeerRef, error) {
	adapter, err := r.forAccount(accountID)
	if err != nil {
		return nil, err
	}
	return adapter.ListPeers(accountID)
}

func (r *Registry) forRoute(routeID string) (Adapter, error) {
	route, err := r.store.Route(routeID)
	if err != nil {
		return nil, err
	}
	return r.forAccount(route.AccountID)
}

func (r *Registry) forAccount(accountID string) (Adapter, error) {
	account, _, err := r.store.AccountCredentials(accountID)
	if err != nil {
		return nil, err
	}
	platform := account.Platform
	if platform == "" {
		platform = Telegram
	}
	r.mu.RLock()
	adapter := r.items[platform]
	r.mu.RUnlock()
	if adapter == nil {
		return nil, fmt.Errorf("connector %q is not installed", platform)
	}
	return adapter, nil
}
