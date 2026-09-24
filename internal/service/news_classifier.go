package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"fri.local/football-reputation-index/internal/domain"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

// Article relevance, as judged by the classifier.
const (
	aboutSubject     = "subject"      // the article is about this player
	aboutMentioned   = "mentioned"    // the player appears, but the story is about someone or something else
	aboutOtherPerson = "other_person" // a namesake: Brahim Díaz under Luis Díaz
	aboutNotFootball = "not_football" // wrong sport, wrong subject entirely
)

// newsEvents are the discrete, reputation-moving things an article can
// report. They feed the Vote on Events page: each is proposed with a delta
// and fans vote on how much it should really count.
var newsEvents = map[string]struct {
	delta     float64
	component string
	autoApply bool
}{
	"arrest_or_charge":            {-7, "character", false},
	"doping":                      {-8, "character", false},
	"racism_or_abuse":             {-6, "character", false},
	"violent_conduct":             {-3, "character", false},
	"red_card":                    {-1.5, "character", false},
	"suspension":                  {-2.5, "character", false},
	"fine":                        {-1, "character", false},
	"public_row":                  {-1.5, "character", false},
	"controversy":                 {-1.5, "character", false},
	"charity_or_sporting_gesture": {1.5, "character", false},
	"hat_trick":                   {2, "performance", false},
	"decisive_goal":               {1, "performance", false},
	"award":                       {3, "performance", false},
	"trophy":                      {4, "performance", false},
	"record":                      {2, "performance", false},
	"costly_error":                {-1.5, "performance", false},
	"serious_injury":              {-1.5, "performance", true},
}

// ArticleVerdict is what the classifier says about one article for one
// player; see domain.ArticleVerdict.
type ArticleVerdict = domain.ArticleVerdict

// articleClassifier reads articles about a player and returns a verdict for
// each, in the same order.
type articleClassifier interface {
	Model() string
	Classify(ctx context.Context, player domain.PlayerSyncTarget, articles []domain.MediaArticleCandidate) ([]ArticleVerdict, error)
}

// NewNewsClassifier returns a Claude-backed classifier, or nil when no key
// is configured — the media sync then falls back to the keyword filters.
func NewNewsClassifier(apiKey, model string) articleClassifier {
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	if strings.TrimSpace(model) == "" {
		model = "claude-opus-5"
	}
	return newClaudeNewsClassifier(apiKey, model)
}

func newClaudeNewsClassifier(apiKey, model string, opts ...option.RequestOption) *claudeNewsClassifier {
	opts = append([]option.RequestOption{option.WithAPIKey(strings.TrimSpace(apiKey))}, opts...)
	return &claudeNewsClassifier{client: anthropic.NewClient(opts...), model: model}
}

type claudeNewsClassifier struct {
	client anthropic.Client
	model  string
}

func (c *claudeNewsClassifier) Model() string { return c.model }

// The system prompt is fixed, so it caches: every call after the first
// reads it back at a tenth of the input price.
const newsClassifierSystem = `You judge football news for a player reputation index.

For each numbered article you are given the player it was found for. Decide:

about:
- "subject": the article is mainly about this player — something they did, said, suffered, won, or a move involving them.
- "mentioned": the player appears but the story is about someone or something else (a teammate, a club, a comparison, a transfer list that names many players, a match report centred on others).
- "other_person": the name matches but it is a different person — a namesake footballer (e.g. Brahim Díaz for Luis Díaz, Lisandro Martínez for Lautaro Martínez), a relative, or an athlete in another sport.
- "not_football": not about football at all.

impact: the effect on THIS player's public reputation, from -3 (seriously damaging: arrest, doping, abuse) through 0 (neutral: routine transfer talk, injury updates, quotes about others) to +3 (major credit: trophy-winning performance, award, record). Judge the player, not the club: "Messi scores but Miami lose again" is mildly positive for Messi. Being fined, suspended, sent off or criticised is negative even when the headline word looks positive. Speculation and rumours are close to 0.

event: set only when the article reports a concrete thing that happened to or was done by this player; otherwise "none". Rumours, predictions and opinions are never events.

Be strict about identity: when unsure whether it is the same person, use "other_person".`

// verdictSchema is the structured-output shape. Structured outputs rather
// than a forced tool call: Claude Opus 5 thinks by default, and a forced
// tool_choice is incompatible with thinking.
func verdictSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verdicts": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"index":  map[string]any{"type": "integer"},
						"about":  map[string]any{"type": "string", "enum": []string{aboutSubject, aboutMentioned, aboutOtherPerson, aboutNotFootball}},
						"impact": map[string]any{"type": "integer", "enum": []int{-3, -2, -1, 0, 1, 2, 3}},
						"event":  map[string]any{"type": "string", "enum": verdictEventEnum()},
						"reason": map[string]any{"type": "string"},
					},
					"required":             []string{"index", "about", "impact", "event", "reason"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"verdicts"},
		"additionalProperties": false,
	}
}

func verdictEventEnum() []string {
	out := []string{"none"}
	for k := range newsEvents {
		out = append(out, k)
	}
	return out
}

func (c *claudeNewsClassifier) Classify(ctx context.Context, player domain.PlayerSyncTarget, articles []domain.MediaArticleCandidate) ([]ArticleVerdict, error) {
	if len(articles) == 0 {
		return nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Player: %s — %s, %s.\n\n", player.Name, player.Club, player.Position)
	for i, a := range articles {
		summary := a.Summary
		if len(summary) > 500 {
			summary = summary[:500]
		}
		fmt.Fprintf(&b, "[%d] %s\n%s\n(source: %s)\n\n", i, a.Title, summary, a.Source)
	}

	resp, err := c.client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:     c.model,
		MaxTokens: 8000,
		System: []anthropic.BetaTextBlockParam{{
			Text:         newsClassifierSystem,
			CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		}},
		// Classification is routine work: low effort keeps it quick and cheap.
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffortLow,
			Format: anthropic.BetaJSONOutputFormatParam{Schema: verdictSchema()},
		},
		// If the model declines, the server re-answers with a fallback model
		// chosen by refusal category, inside the same call.
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(b.String()))},
	})
	if err != nil {
		return nil, err
	}
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		return nil, errors.New("classifier declined")
	}
	for _, block := range resp.Content {
		text, ok := block.AsAny().(anthropic.BetaTextBlock)
		if !ok {
			continue
		}
		var payload struct {
			Verdicts []struct {
				Index  int    `json:"index"`
				About  string `json:"about"`
				Impact int    `json:"impact"`
				Event  string `json:"event"`
				Reason string `json:"reason"`
			} `json:"verdicts"`
		}
		if err := json.Unmarshal([]byte(text.Text), &payload); err != nil {
			return nil, fmt.Errorf("classifier output: %w", err)
		}
		out := make([]ArticleVerdict, len(articles))
		seen := make([]bool, len(articles))
		for _, v := range payload.Verdicts {
			if v.Index < 0 || v.Index >= len(articles) {
				continue
			}
			event := v.Event
			if event == "none" {
				event = ""
			}
			out[v.Index] = ArticleVerdict{About: v.About, Impact: float64(v.Impact), Event: event, Reason: v.Reason}
			seen[v.Index] = true
		}
		for i, ok := range seen {
			if !ok {
				return nil, fmt.Errorf("classifier skipped article %d", i)
			}
		}
		return out, nil
	}
	return nil, errors.New("classifier returned no verdicts")
}
