package aiprocessor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"tgworkbench/internal/domain"
)

func TestTruncate(t *testing.T) {
	t.Parallel()
	if got := truncate("中文abc", 3); got != "中文a" {
		t.Fatalf("truncate() = %q", got)
	}
}

func TestProcessOpenAICompatibleResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": `{"decision":"review","text":"改写","reason":"价格不确定","tags":["price"]}`}}}})
	}))
	defer server.Close()
	result, err := Process(context.Background(), domain.AISettings{BaseURL: server.URL + "/v1", Model: "test", TimeoutSeconds: 2, MaxInputChars: 1000}, "secret", "replace brand", "input")
	if err != nil {
		t.Fatal(err)
	}
	want := Result{Decision: "review", Text: "改写", Reason: "价格不确定", Tags: []string{"price"}}
	if result.Decision != want.Decision || result.Text != want.Text || result.Reason != want.Reason || len(result.Tags) != 1 || result.Tags[0] != want.Tags[0] {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}
