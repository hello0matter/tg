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

	account, err := s.SaveAccount(domain.AccountInput{Name: "客服号", Phone: "+8613800000000", APIID: 123, ConnectorConfig: map[string]string{"region": "cn"}}, []byte("encrypted"))
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := s.ListAccounts()
	if err != nil || len(accounts) != 1 || accounts[0].Platform != domain.PlatformTelegram || accounts[0].Config["region"] != "cn" || !accounts[0].HasConnectorSecret {
		t.Fatalf("unexpected accounts: %#v, %v", accounts, err)
	}
	route, err := s.SaveRoute(domain.Route{
		Name:               "商品群镜像",
		AccountID:          account.ID,
		Sources:            []domain.PeerRef{{ChatID: -1001, TopicID: 2, Title: "来源"}},
		Targets:            []domain.PeerRef{{ChatID: -1002, TopicID: 3, Title: "目标"}},
		Mode:               "copy",
		Enabled:            true,
		SenderAccountIDs:   []string{account.ID, "backup"},
		SenderFilterMode:   "admins_and_allowlist",
		AllowedSenderIDs:   []int64{42},
		IncludeBots:        true,
		ReverseOwnMessages: true,
		ButtonPolicy:       "as_text",
		AIEnabled:          true,
		AIPrompt:           "replace supplier brand",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveRule(domain.Rule{RouteID: route.ID, Name: "品牌替换", Kind: "replace", Pattern: "A", Replacement: "B", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	routes, err := s.ListRoutes()
	if err != nil || len(routes) != 1 || routes[0].Sources[0].TopicID != 2 || routes[0].Sources[0].Platform != domain.PlatformTelegram || routes[0].SenderAccountIDs[1] != "backup" || routes[0].AllowedSenderIDs[0] != 42 || !routes[0].ReverseOwnMessages || routes[0].AIPrompt != "replace supplier brand" {
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
	first, err := s.ClaimOutbox(domain.PlatformTelegram, time.Minute)
	if err != nil || first.Text != "first" {
		t.Fatalf("first claim = %#v, %v", first, err)
	}
	parallel, err := s.ClaimOutbox(domain.PlatformTelegram, time.Minute)
	if err != nil || parallel.Text != "parallel" {
		t.Fatalf("parallel claim = %#v, %v", parallel, err)
	}
	if _, err := s.ClaimOutbox(domain.PlatformTelegram, time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("blocked claim error = %v", err)
	}
	if err := s.CompleteOutbox(first.ID, "a"); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimOutbox(domain.PlatformTelegram, time.Minute)
	if err != nil || second.Text != "second" {
		t.Fatalf("second claim = %#v, %v", second, err)
	}
}

func TestReactivateDailyLimitedOutbox(t *testing.T) {
	t.Parallel()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.EnqueueOutbox([]domain.OutboxJob{
		{RouteID: "r", Target: domain.PeerRef{ChatID: -1}, Text: "first", OrderKey: "-1:0", DedupeKey: "first", SenderAccountIDs: []string{"a"}},
		{RouteID: "r", Target: domain.PeerRef{ChatID: -1}, Text: "second", OrderKey: "-1:0", DedupeKey: "second", SenderAccountIDs: []string{"a"}},
	}); err != nil {
		t.Fatal(err)
	}
	first, err := s.ClaimOutbox(domain.PlatformTelegram, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeferOutbox(first.ID, domain.OutboxReasonDailyLimit, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimOutbox(domain.PlatformTelegram, time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("daily-limited job did not block its order key: %v", err)
	}
	count, err := s.ReactivateDailyLimitedOutbox()
	if err != nil || count != 1 {
		t.Fatalf("reactivated jobs = %d, %v", count, err)
	}
	reactivated, err := s.ClaimOutbox(domain.PlatformTelegram, time.Minute)
	if err != nil || reactivated.ID != first.ID || reactivated.LastError != "" {
		t.Fatalf("reactivated claim = %#v, %v", reactivated, err)
	}
}

func TestCancelPendingOutboxByRoute(t *testing.T) {
	t.Parallel()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.EnqueueOutbox([]domain.OutboxJob{
		{RouteID: "cancel", Target: domain.PeerRef{ChatID: -1}, Text: "one", OrderKey: "-1:0", DedupeKey: "one", SenderAccountIDs: []string{"a"}},
		{RouteID: "cancel", Target: domain.PeerRef{ChatID: -1}, Text: "two", OrderKey: "-1:0", DedupeKey: "two", SenderAccountIDs: []string{"a"}},
		{RouteID: "keep", Target: domain.PeerRef{ChatID: -2}, Text: "keep", OrderKey: "-2:0", DedupeKey: "keep", SenderAccountIDs: []string{"a"}},
	}); err != nil {
		t.Fatal(err)
	}
	count, err := s.CancelPendingOutbox("cancel")
	if err != nil || count != 2 {
		t.Fatalf("cancelled jobs = %d, %v", count, err)
	}
	cancelled, err := s.ListOutbox("cancelled", 10)
	if err != nil || len(cancelled) != 2 {
		t.Fatalf("cancelled list = %#v, %v", cancelled, err)
	}
	job, err := s.ClaimOutbox(domain.PlatformTelegram, time.Minute)
	if err != nil || job.RouteID != "keep" {
		t.Fatalf("remaining claim = %#v, %v", job, err)
	}
}

func TestOutboxClaimsOnlyRequestedPlatform(t *testing.T) {
	t.Parallel()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.EnqueueOutbox([]domain.OutboxJob{{
		Platform: "webhook", RouteID: "r", Target: domain.PeerRef{Platform: "webhook", ChatID: 1},
		Text: "hello", OrderKey: "webhook:1", DedupeKey: "webhook-job", SenderAccountIDs: []string{"a"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimOutbox(domain.PlatformTelegram, time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("telegram claimed webhook job: %v", err)
	}
	job, err := s.ClaimOutbox("webhook", time.Minute)
	if err != nil || job.Platform != "webhook" {
		t.Fatalf("webhook claim = %#v, %v", job, err)
	}
}

func TestSentOutboxCountSince(t *testing.T) {
	t.Parallel()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.EnqueueOutbox([]domain.OutboxJob{{
		RouteID: "r", Target: domain.PeerRef{ChatID: -1}, Text: "sent", OrderKey: "-1:0", DedupeKey: "sent", SenderAccountIDs: []string{"account"},
	}}); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimOutbox(domain.PlatformTelegram, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteOutbox(job.ID, "account"); err != nil {
		t.Fatal(err)
	}
	count, err := s.SentOutboxCountSince("account", time.Now().Add(-time.Minute))
	if err != nil || count != 1 {
		t.Fatalf("sent outbox count = %d, %v", count, err)
	}
}

func TestIsMappedTargetMessage(t *testing.T) {
	t.Parallel()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.SaveMessageMapping(domain.MessageMapping{RouteID: "route", SourceChatID: -1, SourceMessageID: 10, TargetChatID: -2, TargetMessageID: 20}); err != nil {
		t.Fatal(err)
	}
	mapped, err := s.IsMappedTargetMessage(-2, 20)
	if err != nil || !mapped {
		t.Fatalf("mapped target = %v, %v", mapped, err)
	}
}

func TestSettingsScrubTelegramAPIHash(t *testing.T) {
	t.Parallel()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	settings, err := s.Settings()
	if err != nil {
		t.Fatal(err)
	}
	settings.Telegram = domain.TelegramSettings{APIID: 123456, APIHash: "plaintext", HasAPIHash: true}
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Telegram.APIHash != "" || stored.Telegram.HasAPIHash || stored.Telegram.APIID != 123456 {
		t.Fatalf("stored Telegram settings = %#v", stored.Telegram)
	}
}
