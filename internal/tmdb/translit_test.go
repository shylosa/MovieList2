package tmdb

import "testing"

func TestCyrillicToLatin(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Слово Пацана", "Slovo Patsana"},
		{"Скарпетта", "Skarpetta"},
		{"Rick and Morty", "Rick and Morty"},
		{"Враг", "Vrag"},
	}
	for _, tt := range tests {
		got := cyrillicToLatin(tt.in)
		if got != tt.want {
			t.Errorf("cyrillicToLatin(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTitleSimilarity(t *testing.T) {
	if TitleSimilarity("Enemy", "Enemy") < 0.99 {
		t.Error("exact match should be ~1.0")
	}
	if TitleSimilarity("Enemy", "Enemy") >= 0.85 && TitleSimilarity("Enemy", "Inception") >= 0.85 {
		t.Error("unrelated titles should not both pass verify threshold")
	}
}

func TestSlavicTransliterationDetection(t *testing.T) {
	positives := []string{"Tretyi lishnyi", "Dorozhnoe prikljuchenie", "Nochnoj Rejs", "Zhertva obstoyatelstv", "Moj malenkij angel"}
	for _, title := range positives {
		if score, markers := isLikelySlavicTransliteration(title); score < transliterationMinScore {
			t.Errorf("%q score=%d markers=%v", title, score, markers)
		}
	}
	negatives := []string{"Inception", "The Dark Knight", "You", "Road Trip", "Shrek", "Chicago", "Yellowstone", "The Last of Us", "", "tt1234567", "https://www.imdb.com/title/tt1234567/"}
	for _, title := range negatives {
		if score, markers := isLikelySlavicTransliteration(title); score >= transliterationMinScore {
			t.Errorf("false positive %q score=%d markers=%v", title, score, markers)
		}
	}
}

func TestTransliterationVariants(t *testing.T) {
	tests := []struct{ input, want string }{
		{"Tretyi lishnyi", "Третий лишний"},
		{"Dorozhnoe prikljuchenie", "Дорожное приключение"},
		{"Nochnoj Rejs", "Ночной Рейс"},
		{"Poshchada", "Пощада"},
	}
	for _, tt := range tests {
		variants := transliterationVariants(tt.input)
		if len(variants) == 0 || variants[0] != tt.want {
			t.Errorf("variants(%q)=%v want %q", tt.input, variants, tt.want)
		}
	}
	if got := TransliterationHint("Inception"); got != "" {
		t.Fatalf("English title got hint %q", got)
	}
}
