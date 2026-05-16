package tmdb

import "testing"

func TestCyrillicToLatin(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Слово Пацана", "Slovo Patsana"},
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
