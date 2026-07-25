package rules

import (
	"testing"

	"tgworkbench/internal/domain"
)

func TestEngineAppliesRulesInOrder(t *testing.T) {
	t.Parallel()

	configured := []domain.Rule{
		{ID: "suffix", Order: 30, Kind: "suffix", Enabled: true, Replacement: " | 客服 @my_support", MessageType: "all"},
		{ID: "brand", Order: 10, Kind: "replace", Enabled: true, Pattern: "旧品牌", Replacement: "新品牌", MessageType: "all"},
		{ID: "mention", Order: 20, Kind: "remove_mentions", Enabled: true, Replacement: "@my_support", MessageType: "all"},
	}

	result := (Engine{}).Apply(domain.MessageEnvelope{
		MessageType: "text",
		Text:        "旧品牌活动，请联系 @old_support",
	}, configured)

	want := domain.TransformResult{
		Text:     "新品牌活动，请联系 @my_support | 客服 @my_support",
		Decision: "send",
		Matched:  []string{"brand", "mention", "suffix"},
	}
	if result.Text != want.Text || result.Decision != want.Decision || len(result.Matched) != len(want.Matched) {
		t.Fatalf("unexpected result: %#v", result)
	}
	for i := range want.Matched {
		if result.Matched[i] != want.Matched[i] {
			t.Fatalf("unexpected match order: %#v", result.Matched)
		}
	}
}

func TestEngineStopsAfterReview(t *testing.T) {
	t.Parallel()

	result := (Engine{}).Apply(domain.MessageEnvelope{MessageType: "text", Text: "限时返利"}, []domain.Rule{
		{ID: "review", Order: 1, Kind: "review_if", Enabled: true, Pattern: "返利", MessageType: "all"},
		{ID: "suffix", Order: 2, Kind: "suffix", Enabled: true, Replacement: "不应执行", MessageType: "all"},
	})

	if result.Decision != "review" || result.Text != "限时返利" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestEngineReportsInvalidRegex(t *testing.T) {
	t.Parallel()

	result := (Engine{}).Apply(domain.MessageEnvelope{MessageType: "text", Text: "hello"}, []domain.Rule{
		{ID: "bad", Name: "坏正则", Order: 1, Kind: "regex_replace", Enabled: true, Pattern: "[", MessageType: "all"},
	})

	if result.Decision != "send" || len(result.Warnings) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestRemoveMentionsKeepsEmailAndWordPrefix(t *testing.T) {
	t.Parallel()

	result := (Engine{}).Apply(domain.MessageEnvelope{
		MessageType: "text",
		Text:        "联系 @sales_team，邮箱 a@example.com，编号abc@hidden_user",
	}, []domain.Rule{{ID: "mention", Kind: "remove_mentions", Enabled: true, Replacement: "@service", MessageType: "all"}})

	if result.Text != "联系 @service，邮箱 a@example.com，编号abc@hidden_user" {
		t.Fatalf("unexpected text: %q", result.Text)
	}
}
