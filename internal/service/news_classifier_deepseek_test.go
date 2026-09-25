package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"fri.local/football-reputation-index/internal/domain"
)

func deepSeekReply(w http.ResponseWriter, content, finish string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "c1", "object": "chat.completion", "model": "deepseek-flash",
		"choices": []any{map[string]any{
			"index": 0, "finish_reason": finish,
			"message": map[string]any{"role": "assistant", "content": content},
		}},
		"usage": map[string]any{"prompt_tokens": 900, "prompt_cache_hit_tokens": 800, "completion_tokens": 60},
	})
}

func testDeepSeek(url string) *deepSeekNewsClassifier {
	c := newDeepSeekNewsClassifier("test-key", "", url)
	c.backoff = 0
	return c
}

func TestDeepSeekClassifierRequestAndParsing(t *testing.T) {
	var body map[string]any
	var auth, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		deepSeekReply(w, `{"verdicts":[{"index":1,"about":"subject","impact":-2,"event":"fine","reason":"club fined him"},{"index":0,"about":"other_person","impact":0,"event":"none","reason":"Brahim"}]}`, "stop")
	}))
	defer server.Close()

	got, err := testDeepSeek(server.URL).Classify(context.Background(), domain.PlayerSyncTarget{Name: "F. Valverde", Club: "Real Madrid"},
		[]domain.MediaArticleCandidate{article("Juventus cannot go for Brahim Diaz", "a"), article("Madrid fine Valverde", "b")})
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if got[0].About != aboutOtherPerson || got[0].Event != "" || got[1].Event != "fine" || got[1].Impact != -2 {
		t.Errorf("verdicts = %+v", got)
	}

	if path != "/chat/completions" || auth != "Bearer test-key" {
		t.Errorf("path %q auth %q", path, auth)
	}
	if body["model"] != "deepseek-flash" {
		t.Errorf("model = %v, want the deepseek-flash default", body["model"])
	}
	if rf, _ := body["response_format"].(map[string]any); rf["type"] != "json_object" {
		t.Errorf("response_format = %v, want json_object", body["response_format"])
	}
	if th, _ := body["thinking"].(map[string]any); th["type"] != "disabled" {
		t.Errorf("thinking = %v, want disabled for routine classification", body["thinking"])
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", msgs)
	}
	sys := msgs[0].(map[string]any)["content"].(string)
	// JSON mode requires the word "json" and an example in the prompt.
	if !strings.Contains(sys, "json") || !strings.Contains(sys, `{"verdicts": [`) {
		t.Error("system prompt lacks the json instruction and example DeepSeek's JSON mode needs")
	}
	for event := range newsEvents {
		if !strings.Contains(sys, `"`+event+`"`) {
			t.Errorf("event %q is not offered to the model", event)
		}
	}
}

func TestDeepSeekClassifierRetriesEmptyAndTruncatedAnswers(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			deepSeekReply(w, "", "stop") // documented JSON-mode edge case
		case 2:
			deepSeekReply(w, `{"verdicts":[{"index":0,"ab`, "length")
		default:
			deepSeekReply(w, `{"verdicts":[{"index":0,"about":"subject","impact":1,"event":"none","reason":"x"}]}`, "stop")
		}
	}))
	defer server.Close()

	got, err := testDeepSeek(server.URL).Classify(context.Background(), domain.PlayerSyncTarget{Name: "X"},
		[]domain.MediaArticleCandidate{article("a", "1")})
	if err != nil || len(got) != 1 || got[0].About != aboutSubject {
		t.Fatalf("got %+v, %v after %d calls", got, err, calls.Load())
	}
}

func TestDeepSeekClassifierStopsOnBillingErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":{"message":"Insufficient Balance"}}`, http.StatusPaymentRequired)
	}))
	defer server.Close()

	_, err := testDeepSeek(server.URL).Classify(context.Background(), domain.PlayerSyncTarget{Name: "X"},
		[]domain.MediaArticleCandidate{article("a", "1")})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("err %v after %d calls; an empty balance must not be retried", err, calls.Load())
	}
}

func TestDeepSeekClassifierRejectsIncompleteOrInventedAnswers(t *testing.T) {
	for name, content := range map[string]string{
		"missing article": `{"verdicts":[{"index":0,"about":"subject","impact":1,"event":"none","reason":"x"}]}`,
		"unknown about":   `{"verdicts":[{"index":0,"about":"maybe","impact":1,"event":"none","reason":"x"},{"index":1,"about":"subject","impact":1,"event":"none","reason":"x"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				deepSeekReply(w, content, "stop")
			}))
			defer server.Close()
			if _, err := testDeepSeek(server.URL).Classify(context.Background(), domain.PlayerSyncTarget{Name: "X"},
				[]domain.MediaArticleCandidate{article("a", "1"), article("b", "2")}); err == nil {
				t.Error("want an error, not a silent gap")
			}
		})
	}
}

func TestParseVerdictsClampsImpactAndDropsInventedEvents(t *testing.T) {
	got, err := parseVerdicts(`{"verdicts":[{"index":0,"about":"subject","impact":7,"event":"yellow_card","reason":"x"}]}`, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Impact != 3 || got[0].Event != "" {
		t.Errorf("verdict = %+v, want impact clamped to 3 and no event", got[0])
	}
}

func TestDeepSeekKeyWinsOverClaude(t *testing.T) {
	c := NewNewsClassifier(NewsClassifierConfig{DeepSeekAPIKey: "d", AnthropicAPIKey: "a"})
	if _, ok := c.(*deepSeekNewsClassifier); !ok {
		t.Errorf("classifier = %T, want DeepSeek when both keys are set", c)
	}
	if NewNewsClassifier(NewsClassifierConfig{}) != nil {
		t.Error("no keys must mean no classifier")
	}
}
