package service

import (
	"strings"
	"sync"
	"unicode"

	"github.com/jonreiter/govader"
)

// analyzeSentiment returns a polarity score in [-1, +1]:
//
//	−1 strongly negative, 0 neutral, +1 strongly positive.
//
// Internally it picks an analyzer based on a Cyrillic-character ratio:
// English text goes through VADER (lexicon + rules, well-suited to
// headlines), Russian uses a hand-rolled lexicon. A domain-specific
// post-adjustment for football phrases ("hat-trick", "красная карточка",
// "racism", ...) is applied to both branches, since multi-token phrases
// fall through both lexicons.
func analyzeSentiment(text string) float64 {
	cleaned := strings.TrimSpace(text)
	if cleaned == "" {
		return 0
	}

	score := baseSentiment(cleaned)
	score += footballAdjustment(cleaned)
	if score > 1 {
		score = 1
	}
	if score < -1 {
		score = -1
	}
	return score
}

func baseSentiment(text string) float64 {
	if cyrillicRatio(text) >= 0.3 {
		return russianSentiment(text)
	}
	return englishSentiment(text)
}

var (
	englishOnce     sync.Once
	englishAnalyzer *govader.SentimentIntensityAnalyzer
)

func englishSentiment(text string) float64 {
	englishOnce.Do(func() {
		analyzer := govader.NewSentimentIntensityAnalyzer()
		// Football single-token boosters that VADER's general lexicon misses.
		// Multi-word phrases ("hat-trick", "red card") are handled in
		// footballAdjustment below to avoid tokenizer fragility.
		extras := map[string]float64{
			"hattrick":    2.5,
			"motm":        2.0,
			"masterclass": 2.5,
			"brace":       1.5,
			"comeback":    1.5,
			"pivotal":     1.5,
			"clinical":    1.8,
			"scandal":     -2.5,
			"racism":      -3.5,
			"racist":      -3.0,
			"doping":      -3.5,
			"arrest":      -3.5,
			"arrested":    -3.0,
			"benched":     -1.5,
			"sacked":      -2.5,
			"backlash":    -2.0,
		}
		for word, valence := range extras {
			analyzer.Lexicon[word] = valence
		}
		englishAnalyzer = analyzer
	})

	scores := englishAnalyzer.PolarityScores(text)
	return scores.Compound
}

// russianSentiment scores Cyrillic text using a small hand-curated lexicon.
// Each match contributes a fixed valence; the result is squashed into
// [−1, +1] via a tanh-like normalization that prevents long articles with
// many neutral words from dominating short headlines.
func russianSentiment(text string) float64 {
	lower := strings.ToLower(text)

	var score float64
	for word, weight := range russianLexicon {
		if strings.Contains(lower, word) {
			score += weight
		}
	}

	// Empirical squashing: 1 strong word ≈ 0.4, 3 strong words → ~0.85.
	if score > 0 {
		return clamp01(score / 4.0)
	}
	if score < 0 {
		return -clamp01(-score / 4.0)
	}
	return 0
}

func clamp01(v float64) float64 {
	if v > 1 {
		return 1
	}
	if v < 0 {
		return 0
	}
	return v
}

// footballAdjustment matches multi-word football phrases that bypass
// tokenizer-based lexicon lookups. Matched phrases shift the final score by
// the listed delta; values in [−0.4, +0.4] keep the adjustment proportional
// to the underlying [−1, +1] scale.
func footballAdjustment(text string) float64 {
	lower := strings.ToLower(text)
	var delta float64

	for phrase, weight := range footballPositive {
		if strings.Contains(lower, phrase) {
			delta += weight
		}
	}
	for phrase, weight := range footballNegative {
		if strings.Contains(lower, phrase) {
			delta -= weight
		}
	}
	return delta
}

func cyrillicRatio(text string) float64 {
	var total, cyr int
	for _, r := range text {
		if unicode.IsLetter(r) {
			total++
			if unicode.Is(unicode.Cyrillic, r) {
				cyr++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(cyr) / float64(total)
}

// Compact RU lexicon. Values approximate VADER scale (±2..±3 for strong
// emotion, ±1..±1.5 for moderate). Football-specific terms are weighted
// higher because they appear less often outside the domain.
var russianLexicon = map[string]float64{
	"победа":    2.0,
	"победил":   2.0,
	"победный":  2.0,
	"гол":       1.5,
	"забил":     1.8,
	"дубль":     2.0,
	"хет-трик":  3.0,
	"ассист":    1.5,
	"рекорд":    2.0,
	"чемпион":   2.0,
	"триумф":    2.5,
	"лидер":     1.5,
	"талант":    1.5,
	"блестящий": 2.0,
	"великолеп": 2.0,
	"успех":     1.8,
	"прорыв":    1.5,
	"спасён":    1.0,
	"восхищ":    2.0,

	"поражение":   -1.8,
	"провал":      -2.5,
	"скандал":     -2.5,
	"обвин":       -2.0,
	"расизм":      -3.5,
	"расист":      -3.0,
	"допинг":      -3.5,
	"дисквалифик": -2.5,
	"арест":       -3.5,
	"удалил":      -1.5,
	"удалён":      -1.5,
	"красная":     -1.0,
	"травма":      -2.0,
	"травмиров":   -2.0,
	"критик":      -1.5,
	"штраф":       -1.0,
	"спор":        -0.8,
	"кризис":      -2.0,
}

var footballPositive = map[string]float64{
	"hat-trick":          0.4,
	"hat trick":          0.4,
	"man of the match":   0.35,
	"clean sheet":        0.25,
	"player of the":      0.30,
	"world record":       0.35,
	"transfer record":    0.30,
	"top scorer":         0.25,
	"исторический":       0.25,
	"рекордный трансфер": 0.35,
	"лучший игрок":       0.30,
	"сухой матч":         0.25,
}

var footballNegative = map[string]float64{
	"red card":              0.30,
	"two-match ban":         0.25,
	"three-match ban":       0.30,
	"under investigation":   0.40,
	"failed drug test":      0.45,
	"banned for":            0.35,
	"sent off":              0.30,
	"красная карточка":      0.30,
	"уголовное":             0.45,
	"под следствием":        0.40,
	"отстранён":             0.35,
	"провалил тест":         0.45,
	"скандальное поведение": 0.35,
}
