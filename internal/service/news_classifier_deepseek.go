package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

// DeepSeek speaks the OpenAI chat-completions format, so this is a plain
// HTTP call rather than an SDK: the one non-OpenAI field we need, `thinking`,
// would have to be smuggled through an OpenAI SDK's extra-fields hook anyway.
// See docs/news-classifier.md for why each request field is set as it is.

const (
	defaultDeepSeekBaseURL = "https://api.deepseek.com"
	defaultDeepSeekModel   = "deepseek-flash"
	deepSeekAttempts       = 3
)

type deepSeekNewsClassifier struct {
	http    *http.Client
	baseURL string
	apiKey  string
	model   string
	backoff time.Duration
}

func newDeepSeekNewsClassifier(apiKey, model, baseURL string) *deepSeekNewsClassifier {
	if strings.TrimSpace(model) == "" {
		model = defaultDeepSeekModel
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultDeepSeekBaseURL
	}
	return &deepSeekNewsClassifier{
		// Five articles take a few seconds; the generous ceiling covers a
		// busy hour on DeepSeek's side without holding the sync forever.
		http:    &http.Client{Timeout: 90 * time.Second},
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  strings.TrimSpace(apiKey),
		model:   model,
		backoff: 2 * time.Second,
	}
}

func (c *deepSeekNewsClassifier) Model() string { return c.model }

// JSON mode guarantees valid JSON, not our shape, so the prompt spells the
// shape out with an example (DeepSeek's docs ask for the word "json" and an
// example in the prompt) and parseVerdicts checks every field.
var deepSeekSystemPrompt = newsClassifierSystem + `

Answer in json only, one verdict per article, in this exact form:
{"verdicts": [{"index": 0, "about": "subject", "impact": -2, "event": "suspension", "reason": "banned for three matches after a red card"}]}

"about" is one of: ` + quotedList(aboutSubject, aboutMentioned, aboutOtherPerson, aboutNotFootball) + `.
"impact" is a whole number from -3 to 3.
"event" is one of: ` + quotedList(verdictEventEnum()...) + `.
"reason" is one short sentence in English.`

func quotedList(values ...string) string {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	for i, v := range sorted {
		sorted[i] = `"` + v + `"`
	}
	return strings.Join(sorted, ", ")
}

type deepSeekRequest struct {
	Model          string            `json:"model"`
	Messages       []deepSeekMessage `json:"messages"`
	ResponseFormat struct {
		Type string `json:"type"`
	} `json:"response_format"`
	Thinking struct {
		Type string `json:"type"`
	} `json:"thinking"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`
}

type deepSeekMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type deepSeekResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens         int `json:"prompt_tokens"`
		PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
		CompletionTokens     int `json:"completion_tokens"`
	} `json:"usage"`
}

// errPermanent marks failures a retry cannot fix: a bad key, no balance, a
// malformed request.
type errPermanent struct{ error }

func (c *deepSeekNewsClassifier) Classify(ctx context.Context, player domain.PlayerSyncTarget, articles []domain.MediaArticleCandidate) ([]ArticleVerdict, error) {
	if len(articles) == 0 {
		return nil, nil
	}
	req := deepSeekRequest{
		Model: c.model,
		Messages: []deepSeekMessage{
			// The system prompt never changes, so DeepSeek's automatic
			// prefix cache serves it at the cache-hit price after the first call.
			{Role: "system", Content: deepSeekSystemPrompt},
			{Role: "user", Content: classifierUserPrompt(player, articles)},
		},
		// Sorting articles is routine work; thinking would multiply the
		// output tokens for no better answer. Non-thinking mode also honours
		// temperature, and 0 keeps verdicts stable between syncs.
		Temperature: 0,
		// About 60 tokens a verdict; the headroom keeps JSON from being cut
		// off mid-array, which is DeepSeek's documented failure mode.
		MaxTokens: 400 + 150*len(articles),
	}
	req.ResponseFormat.Type = "json_object"
	req.Thinking.Type = "disabled"

	var lastErr error
	for attempt := 0; attempt < deepSeekAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.backoff * time.Duration(attempt)):
			}
		}
		verdicts, err := c.classifyOnce(ctx, req, len(articles))
		if err == nil {
			return verdicts, nil
		}
		var permanent errPermanent
		if errors.As(err, &permanent) {
			return nil, fmt.Errorf("deepseek: %w", err)
		}
		lastErr = err
	}
	return nil, fmt.Errorf("deepseek: %d attempts: %w", deepSeekAttempts, lastErr)
}

func (c *deepSeekNewsClassifier) classifyOnce(ctx context.Context, req deepSeekRequest, n int) ([]ArticleVerdict, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, errPermanent{err}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, errPermanent{err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, snippet(raw))
	default:
		// 400 bad request, 401 bad key, 402 out of balance, 422 bad params.
		return nil, errPermanent{fmt.Errorf("http %d: %s", resp.StatusCode, snippet(raw))}
	}

	var out deepSeekResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("response: %w", err)
	}
	if len(out.Choices) == 0 {
		return nil, errors.New("no choices")
	}
	choice := out.Choices[0]
	log.Printf("news: deepseek %d articles, %d prompt tokens (%d cached), %d out, finish %s",
		n, out.Usage.PromptTokens, out.Usage.PromptCacheHitTokens, out.Usage.CompletionTokens, choice.FinishReason)
	switch choice.FinishReason {
	case "stop":
	case "content_filter":
		return nil, errPermanent{errors.New("declined by content filter")}
	default:
		// "length" truncates the JSON; "insufficient_system_resource" is a
		// busy server. Both are worth another go.
		return nil, fmt.Errorf("finish_reason %q", choice.FinishReason)
	}
	// JSON mode can come back empty — a documented DeepSeek edge case.
	if strings.TrimSpace(choice.Message.Content) == "" {
		return nil, errors.New("empty content")
	}
	return parseVerdicts(choice.Message.Content, n)
}

// parseVerdicts checks what JSON mode does not: every article answered once,
// every field inside its allowed values.
func parseVerdicts(content string, n int) ([]ArticleVerdict, error) {
	var payload struct {
		Verdicts []struct {
			Index  int     `json:"index"`
			About  string  `json:"about"`
			Impact float64 `json:"impact"`
			Event  string  `json:"event"`
			Reason string  `json:"reason"`
		} `json:"verdicts"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil, fmt.Errorf("verdicts: %w", err)
	}
	out := make([]ArticleVerdict, n)
	seen := make([]bool, n)
	for _, v := range payload.Verdicts {
		if v.Index < 0 || v.Index >= n {
			continue
		}
		switch v.About {
		case aboutSubject, aboutMentioned, aboutOtherPerson, aboutNotFootball:
		default:
			return nil, fmt.Errorf("article %d: unknown about %q", v.Index, v.About)
		}
		impact := v.Impact
		if impact < -3 {
			impact = -3
		} else if impact > 3 {
			impact = 3
		}
		event := v.Event
		if _, ok := newsEvents[event]; !ok {
			// "none", or an event name the model invented: no event.
			event = ""
		}
		out[v.Index] = ArticleVerdict{About: v.About, Impact: impact, Event: event, Reason: v.Reason}
		seen[v.Index] = true
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("article %d not answered", i)
		}
	}
	return out, nil
}

func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
