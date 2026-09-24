package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"

	"fri.local/football-reputation-index/internal/domain"
)

type scriptedClassifier struct {
	verdicts map[string]domain.ArticleVerdict // by title
	calls    int
	seen     int
	fail     bool
}

func (c *scriptedClassifier) Model() string { return "test-model" }
func (c *scriptedClassifier) Classify(_ context.Context, _ domain.PlayerSyncTarget, articles []domain.MediaArticleCandidate) ([]domain.ArticleVerdict, error) {
	c.calls++
	c.seen += len(articles)
	if c.fail {
		return nil, errors.New("down")
	}
	out := make([]domain.ArticleVerdict, len(articles))
	for i, a := range articles {
		out[i] = c.verdicts[a.Title]
	}
	return out, nil
}

type verdictRepo struct {
	mockRepo
	cache map[string]domain.ArticleVerdict
}

func (r *verdictRepo) ArticleVerdicts(_ context.Context, _ int64, keys []string) (map[string]domain.ArticleVerdict, error) {
	out := map[string]domain.ArticleVerdict{}
	for _, k := range keys {
		if v, ok := r.cache[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}
func (r *verdictRepo) SaveArticleVerdicts(_ context.Context, _ int64, _ string, v map[string]domain.ArticleVerdict) error {
	for k, x := range v {
		r.cache[k] = x
	}
	return nil
}

func article(title, url string) domain.MediaArticleCandidate {
	return domain.MediaArticleCandidate{Title: title, SourceURL: url, Source: "bbc", PublishedAt: time.Now()}
}

func TestClassifierKeepsThePlayerAndDropsNamesakes(t *testing.T) {
	// Real feed lines from 2026-09-24: Brahim Díaz filed under Luis Díaz,
	// Real Madrid fining Valverde scored positive because of the word "fine".
	cls := &scriptedClassifier{verdicts: map[string]domain.ArticleVerdict{
		"Juventus cannot go for Brahim Diaz":         {About: aboutOtherPerson},
		"Carragher applauds Luis Diaz":               {About: aboutSubject, Impact: 2},
		"Bayern transfer list names Diaz and others": {About: aboutMentioned, Impact: 0},
		"Diaz sent off in Klassiker":                 {About: aboutSubject, Impact: -2, Event: "red_card"},
	}}
	repo := &verdictRepo{cache: map[string]domain.ArticleVerdict{}}
	svc := New(repo, nil, nil, nil).WithNewsClassifier(cls)
	player := domain.PlayerSyncTarget{ID: 28, Name: "L. Díaz", Club: "Bayern Munich"}

	in := []domain.MediaArticleCandidate{
		article("Bayern transfer list names Diaz and others", "u1"),
		article("Juventus cannot go for Brahim Diaz", "u2"),
		article("Carragher applauds Luis Diaz", "u3"),
		article("Diaz sent off in Klassiker", "u4"),
	}
	out, events := svc.judgeArticles(context.Background(), player, in)

	if len(out) != 3 {
		t.Fatalf("kept %d, want 3 — the namesake must go", len(out))
	}
	for _, a := range out {
		if a.Title == "Juventus cannot go for Brahim Diaz" {
			t.Error("Brahim Díaz is still filed under Luis Díaz")
		}
	}
	if out[len(out)-1].Verdict.About != aboutMentioned {
		t.Error("a passing mention was ranked above stories about the player")
	}
	if len(events) != 1 || events[0].TriggerWord != "red_card" || events[0].SourceRef != "article:u4" {
		t.Fatalf("events = %+v, want one red_card keyed to the article", events)
	}
	if events[0].AutoApply {
		t.Error("a red card should go to the fans' vote, not apply itself")
	}

	// Second sync: everything is cached, nothing is paid for again.
	callsBefore := cls.calls
	svc.judgeArticles(context.Background(), player, in)
	if cls.calls != callsBefore {
		t.Error("already-judged articles were sent to the classifier again")
	}
}

func TestVerdictImpactReplacesWordListSentiment(t *testing.T) {
	svc := New(&mockRepo{}, nil, nil, nil)
	svc.mediaProvider = fixedArticlesProvider{}
	a := article("Madrid Fine Valverde, Tchouameni €500,000 Each For Bust-Up", "u9")
	a.Verdict = &domain.ArticleVerdict{About: aboutSubject, Impact: -2}
	res := svc.buildMediaSyncResult(domain.PlayerSyncTarget{ID: 18, Name: "F. Valverde"}, []domain.MediaArticleCandidate{a})
	if got := res.Articles[0].Sentiment; got >= 0 {
		t.Errorf("sentiment = %v — a fine for a bust-up must read negative", got)
	}
}

func TestClassifierOutageLeavesUnjudgedArticlesOut(t *testing.T) {
	// When the classifier is down, articles already judged still count and
	// new ones wait for the next run rather than being scored by guesswork.
	cls := &scriptedClassifier{fail: true}
	repo := &verdictRepo{cache: map[string]domain.ArticleVerdict{
		"old": {About: aboutSubject, Impact: 1},
	}}
	svc := New(repo, nil, nil, nil).WithNewsClassifier(cls)
	out, _ := svc.judgeArticles(context.Background(), domain.PlayerSyncTarget{ID: 1, Name: "X"},
		[]domain.MediaArticleCandidate{article("old story", "old"), article("new story", "new")})
	if len(out) != 1 || out[0].SourceURL != "old" {
		t.Errorf("kept %+v, want only the already-judged article", out)
	}
}

func TestNoClassifierChangesNothing(t *testing.T) {
	svc := New(&mockRepo{}, nil, nil, nil)
	in := []domain.MediaArticleCandidate{article("a", "1"), article("b", "2")}
	out, events := svc.judgeArticles(context.Background(), domain.PlayerSyncTarget{ID: 1}, in)
	if len(out) != 2 || events != nil {
		t.Error("without a classifier the articles must pass through untouched")
	}
}

func TestEventTypesAreAllInTheSchema(t *testing.T) {
	enum := verdictEventEnum()
	if len(enum) != len(newsEvents)+1 {
		t.Errorf("schema enum has %d values, want %d", len(enum), len(newsEvents)+1)
	}
}

func TestClaudeClassifierRequestAndParsing(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		verdicts := `{"verdicts":[{"index":1,"about":"subject","impact":-2,"event":"fine","reason":"club fined him"},{"index":0,"about":"other_person","impact":0,"event":"none","reason":"Brahim"}]}`
		resp := map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5",
			"content":     []any{map[string]any{"type": "text", "text": verdicts}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 10},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := newClaudeNewsClassifier("test-key", "claude-opus-5", option.WithBaseURL(server.URL))
	got, err := c.Classify(context.Background(), domain.PlayerSyncTarget{Name: "F. Valverde", Club: "Real Madrid"},
		[]domain.MediaArticleCandidate{article("Juventus cannot go for Brahim Diaz", "a"), article("Madrid fine Valverde", "b")})
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	// Verdicts come back out of order; they must land on the right article.
	if got[0].About != aboutOtherPerson || got[1].Event != "fine" || got[1].Impact != -2 {
		t.Errorf("verdicts = %+v", got)
	}

	oc, _ := body["output_config"].(map[string]any)
	if oc["effort"] != "low" {
		t.Errorf("effort = %v, want low", oc["effort"])
	}
	if f, _ := oc["format"].(map[string]any); f["type"] != "json_schema" || f["schema"] == nil {
		t.Errorf("format = %v, want a json_schema", oc["format"])
	}
	if _, forced := body["tool_choice"]; forced {
		t.Error("forced tool_choice is incompatible with thinking on Opus 5")
	}
	if body["fallbacks"] != "default" {
		t.Errorf("fallbacks = %v, want the server-side default", body["fallbacks"])
	}
	sys, _ := body["system"].([]any)
	if len(sys) != 1 || sys[0].(map[string]any)["cache_control"] == nil {
		t.Error("the fixed system prompt should be cached")
	}
}

func TestClaudeClassifierRejectsIncompleteAnswers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5",
			"content":     []any{map[string]any{"type": "text", "text": `{"verdicts":[{"index":0,"about":"subject","impact":1,"event":"none","reason":"x"}]}`}},
			"stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()
	c := newClaudeNewsClassifier("k", "claude-opus-5", option.WithBaseURL(server.URL))
	if _, err := c.Classify(context.Background(), domain.PlayerSyncTarget{Name: "X"},
		[]domain.MediaArticleCandidate{article("a", "1"), article("b", "2")}); err == nil {
		t.Error("a verdict list missing an article must be an error, not a silent gap")
	}
}

func TestWordListReadsFinesAsNegative(t *testing.T) {
	// Only matters without a classifier, but it was scoring a club fine for
	// a dressing-room fight as good news.
	if got := sentimentScore("Madrid Fine Valverde, Tchouameni €500,000 Each For Bust-Up"); got >= 0 {
		t.Errorf("sentiment = %v, want negative", got)
	}
}
