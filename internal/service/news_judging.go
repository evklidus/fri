package service

import (
	"context"
	"log"
	"sort"

	"fri.local/football-reputation-index/internal/domain"
)

// verdictStore caches classifier verdicts.
type verdictStore interface {
	ArticleVerdicts(ctx context.Context, playerID int64, keys []string) (map[string]domain.ArticleVerdict, error)
	SaveArticleVerdicts(ctx context.Context, playerID int64, model string, verdicts map[string]domain.ArticleVerdict) error
}

// WithNewsClassifier plugs in the article classifier. nil keeps the keyword
// filters and word-list sentiment.
func (s *Service) WithNewsClassifier(c articleClassifier) *Service {
	s.newsClassifier = c
	return s
}

// judgeArticles has the classifier read a player's candidate articles and
// returns the ones that are really about this player — the player as the
// subject first, then passing mentions — each carrying its verdict, plus the
// events the articles report.
//
// Without a classifier the articles come back unchanged: the keyword
// filters upstream have already run, and word-list sentiment applies.
func (s *Service) judgeArticles(ctx context.Context, player domain.PlayerSyncTarget, articles []domain.MediaArticleCandidate) ([]domain.MediaArticleCandidate, []domain.CharacterEventCandidate) {
	if s.newsClassifier == nil || len(articles) == 0 {
		return articles, nil
	}
	store, _ := s.repo.(verdictStore)

	keys := make([]string, len(articles))
	for i, a := range articles {
		keys[i] = domain.NewsArticleKey(a.SourceURL, a.Title)
	}
	cached := map[string]domain.ArticleVerdict{}
	if store != nil {
		if got, err := store.ArticleVerdicts(ctx, player.ID, keys); err == nil {
			cached = got
		} else {
			log.Printf("news: verdict cache read failed for %s: %v", player.Name, err)
		}
	}

	var fresh []domain.MediaArticleCandidate
	var freshIdx []int
	for i, a := range articles {
		if _, ok := cached[keys[i]]; !ok {
			fresh = append(fresh, a)
			freshIdx = append(freshIdx, i)
		}
	}
	if len(fresh) > 0 {
		verdicts, err := s.newsClassifier.Classify(ctx, player, fresh)
		if err != nil {
			// Keep going on what is cached; unjudged articles are left out
			// rather than scored by guesswork this run, and are judged next time.
			log.Printf("news: classifier failed for %s: %v", player.Name, err)
		} else {
			saved := make(map[string]domain.ArticleVerdict, len(verdicts))
			for j, v := range verdicts {
				key := keys[freshIdx[j]]
				cached[key] = v
				saved[key] = v
			}
			if store != nil {
				if err := store.SaveArticleVerdicts(ctx, player.ID, s.newsClassifier.Model(), saved); err != nil {
					log.Printf("news: verdict cache write failed for %s: %v", player.Name, err)
				}
			}
		}
	}

	type judged struct {
		article domain.MediaArticleCandidate
		rank    int
	}
	var kept []judged
	var events []domain.CharacterEventCandidate
	for i, a := range articles {
		v, ok := cached[keys[i]]
		if !ok {
			continue
		}
		switch v.About {
		case aboutSubject, aboutMentioned:
		default:
			continue // a namesake, another sport, not football
		}
		verdict := v
		a.Verdict = &verdict
		rank := 0
		if v.About == aboutMentioned {
			rank = 1
		}
		kept = append(kept, judged{a, rank})

		if v.About == aboutSubject && v.Event != "" {
			if spec, ok := newsEvents[v.Event]; ok {
				events = append(events, domain.CharacterEventCandidate{
					PlayerID:        player.ID,
					TriggerWord:     v.Event,
					Delta:           spec.delta,
					TargetComponent: spec.component,
					// One event per article per kind, however many syncs see it.
					SourceRef: "article:" + keys[i],
					AutoApply: spec.autoApply,
				})
			}
		}
	}
	// Stories about the player first, then passing mentions; the provider's
	// recency order is kept inside each group.
	sort.SliceStable(kept, func(a, b int) bool { return kept[a].rank < kept[b].rank })
	out := make([]domain.MediaArticleCandidate, len(kept))
	for i, k := range kept {
		out[i] = k.article
	}
	return out, events
}
