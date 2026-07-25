package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"tgworkbench/internal/domain"
)

func TestRouteAndRuleRoundTrip(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	account, err := s.SaveAccount(domain.AccountInput{Name: "客服号", Phone: "+8613800000000", APIID: 123}, []byte("encrypted"))
	if err != nil {
		t.Fatal(err)
	}
	route, err := s.SaveRoute(domain.Route{
		Name:             "商品群镜像",
		AccountID:        account.ID,
		Sources:          []domain.PeerRef{{ChatID: -1001, TopicID: 2, Title: "来源"}},
		Targets:          []domain.PeerRef{{ChatID: -1002, TopicID: 3, Title: "目标"}},
		Mode:             "copy",
		Enabled:          true,
		SenderAccountIDs: []string{account.ID, "backup"},
		SenderFilterMode: "admins_and_allowlist",
		AllowedSenderIDs: []int64{42},
		IncludeBots:      true,
		ButtonPolicy:     "as_text",
		AIEnabled:        true,
		AIPrompt:         "replace supplier brand",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveRule(domain.Rule{RouteID: route.ID, Name: "品牌替换", Kind: "replace", Pattern: "A", Replacement: "B", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	routes, err := s.ListRoutes()
	if err != nil || len(routes) != 1 || routes[0].Sources[0].TopicID != 2 || routes[0].SenderAccountIDs[1] != "backup" || routes[0].AllowedSenderIDs[0] != 42 || routes[0].AIPrompt != "replace supplier brand" {
		t.Fatalf("unexpected routes: %#v, %v", routes, err)
	}
	rules, err := s.ListRules(route.ID)
	if err != nil || len(rules) != 1 || rules[0].Replacement != "B" {
		t.Fatalf("unexpected rules: %#v, %v", rules, err)
	}
}

func TestOutboxPreservesOrderPerTarget(t *testing.T) {
	t.Parallel()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	jobs := []domain.OutboxJob{
		{RouteID: "r", Target: domain.PeerRef{ChatID: -1}, Text: "first", OrderKey: "-1:0", DedupeKey: "first", SenderAccountIDs: []string{"a"}},
		{RouteID: "r", Target: domain.PeerRef{ChatID: -1}, Text: "second", OrderKey: "-1:0", DedupeKey: "second", SenderAccountIDs: []string{"a"}},
		{RouteID: "r", Target: domain.PeerRef{ChatID: -2}, Text: "parallel", OrderKey: "-2:0", DedupeKey: "parallel", SenderAccountIDs: []string{"a"}},
	}
	if err := s.EnqueueOutbox(jobs); err != nil {
		t.Fatal(err)
	}
	first, err := s.ClaimOutbox(time.Minute)
	if err != nil || first.Text != "first" {
		t.Fatalf("first claim = %#v, %v", first, err)
	}
	parallel, err := s.ClaimOutbox(time.Minute)
	if err != nil || parallel.Text != "parallel" {
		t.Fatalf("parallel claim = %#v, %v", parallel, err)
	}
	if _, err := s.ClaimOutbox(time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("blocked claim error = %v", err)
	}
	if err := s.CompleteOutbox(first.ID, "a"); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimOutbox(time.Minute)
	if err != nil || second.Text != "second" {
		t.Fatalf("second claim = %#v, %v", second, err)
	}
}
