package service

import "testing"

func TestAnalyzeSentimentPolarityClasses(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string // "pos" / "neg" / "neu"
	}{
		{"english_positive_goal", "Haaland scored a brilliant brace in a masterclass display", "pos"},
		{"english_strong_negative_doping", "Haaland faces doping investigation after failed drug test", "neg"},
		{"english_red_card", "Player sent off after a red card to seal the loss", "neg"},
		{"english_neutral", "Match scheduled for Saturday at the stadium", "neu"},
		{"russian_positive_record", "Месси установил исторический рекорд и забил победный гол", "pos"},
		{"russian_negative_scandal", "Громкий скандал и обвинения в расизме омрачают карьеру", "neg"},
		{"russian_red_card", "Защитник получил красную карточку и удалён с поля", "neg"},
		{"empty_string", "", "neu"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			score := analyzeSentiment(tc.text)
			got := sentimentImpactType(score)
			if got != tc.want {
				t.Errorf("analyzeSentiment(%q) = %.3f → %q, want %q", tc.text, score, got, tc.want)
			}
			if score < -1 || score > 1 {
				t.Errorf("score %.3f out of [-1, +1] range", score)
			}
		})
	}
}

func TestAnalyzeSentimentFootballPhraseAdjustment(t *testing.T) {
	plain := analyzeSentiment("Player scored against rivals")
	withHatTrick := analyzeSentiment("Player scored a hat-trick against rivals")
	if withHatTrick <= plain {
		t.Errorf("hat-trick must boost positive score: plain=%.3f, hat-trick=%.3f", plain, withHatTrick)
	}

	plainNeg := analyzeSentiment("The defender struggled in the match")
	withRedCard := analyzeSentiment("The defender struggled and got a red card in the match")
	if withRedCard >= plainNeg {
		t.Errorf("red card must drag score down: plain=%.3f, red-card=%.3f", plainNeg, withRedCard)
	}
}

func TestCyrillicRatioDetection(t *testing.T) {
	cases := []struct {
		text      string
		minRatio  float64
		isRussian bool
	}{
		{"Lionel Messi scored", 0, false},
		{"Месси забил гол", 0.5, true},
		{"Mixed: Месси scored", 0.3, true},
	}
	for _, tc := range cases {
		got := cyrillicRatio(tc.text)
		if got < tc.minRatio {
			t.Errorf("cyrillicRatio(%q) = %.2f, want ≥ %.2f", tc.text, got, tc.minRatio)
		}
	}
}

func TestNormalizeSentimentMapsToPercentScale(t *testing.T) {
	cases := []struct {
		input float64
		want  float64
	}{
		{-1, 0},
		{0, 50},
		{1, 100},
		{0.5, 75},
		{-0.5, 25},
	}
	for _, tc := range cases {
		if got := normalizeSentiment(tc.input); got != tc.want {
			t.Errorf("normalizeSentiment(%v) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestSentimentImpactDeltaBudget(t *testing.T) {
	cases := []struct {
		score float64
		want  float64
	}{
		{1, 3},
		{-1, -3},
		{0, 0},
		{0.5, 1.5},
		{-0.5, -1.5},
	}
	for _, tc := range cases {
		if got := sentimentImpactDelta(tc.score); got != tc.want {
			t.Errorf("sentimentImpactDelta(%v) = %v, want %v", tc.score, got, tc.want)
		}
	}
}
