package store

import (
	"path/filepath"
	"testing"

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
		Name:      "商品群镜像",
		AccountID: account.ID,
		Sources:   []domain.PeerRef{{ChatID: -1001, TopicID: 2, Title: "来源"}},
		Targets:   []domain.PeerRef{{ChatID: -1002, TopicID: 3, Title: "目标"}},
		Mode:      "copy",
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveRule(domain.Rule{RouteID: route.ID, Name: "品牌替换", Kind: "replace", Pattern: "A", Replacement: "B", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	routes, err := s.ListRoutes()
	if err != nil || len(routes) != 1 || routes[0].Sources[0].TopicID != 2 {
		t.Fatalf("unexpected routes: %#v, %v", routes, err)
	}
	rules, err := s.ListRules(route.ID)
	if err != nil || len(rules) != 1 || rules[0].Replacement != "B" {
		t.Fatalf("unexpected rules: %#v, %v", rules, err)
	}
}
