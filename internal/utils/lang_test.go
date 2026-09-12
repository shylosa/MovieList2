package utils

import "testing"

func TestCyrillicToLatinDoubleConsonants(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Скарпетта", "Skarpetta"}, // тт → tt
		{"Мадонна", "Madonna"},     // нн → nn
		{"Галла", "Galla"},         // лл → ll
		{"Рассел", "Rassel"},       // сс → ss
	}
	for _, tt := range tests {
		got := CyrillicToLatin(tt.input)
		if got != tt.expected {
			t.Errorf("CyrillicToLatin(%q) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}

func TestCyrillicToLatinPassthrough(t *testing.T) {
	if got := CyrillicToLatin("Rick and Morty"); got != "Rick and Morty" {
		t.Errorf("non-Cyrillic passthrough = %q", got)
	}
}

func TestDetectTextLanguage(t *testing.T) {
	tests := map[string]TextLanguage{
		"Суперкопы 80":    LanguageRussian,
		"Любимые фильмы":  LanguageRussian,
		"Улюблені фільми": LanguageUkrainian,
		"Нічний рейс":     LanguageUkrainian,
		"Super Troopers":  LanguageEnglish,
		"":                LanguageUnknown,
	}
	for text, want := range tests {
		if got := DetectTextLanguage(text); got != want {
			t.Errorf("DetectTextLanguage(%q)=%q, want %q", text, got, want)
		}
	}
}
