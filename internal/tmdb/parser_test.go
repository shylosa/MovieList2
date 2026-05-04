package tmdb

import (
	"reflect"
	"testing"
)

func TestParseFilename(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected ParsedFile
	}{
		{
			name:  "Стандартний фільм з роком",
			input: "D:/Movies/Inception.2010.1080p.mkv",
			expected: ParsedFile{
				OriginalName: "Inception.2010.1080p",
				CleanTitle:   "Inception",
				Year:         2010,
				MediaType:    MediaTypeMovie,
				TitleLang:    TitleLangLatin,
				ParentDir:    "Movies",
			},
		},
		{
			name:  "Серіал стандартний",
			input: "Rick.and.Morty.S07E05.WEBRip.mkv",
			expected: ParsedFile{
				OriginalName: "Rick.and.Morty.S07E05.WEBRip",
				CleanTitle:   "Rick and Morty",
				MediaType:    MediaTypeTV,
				TitleLang:    TitleLangLatin,
			},
		},
		{
			name:  "Складний випадок: Серіал маркер НА ПОЧАТКУ",
			input: "S02E04.The.Boys.1080p.mkv",
			expected: ParsedFile{
				OriginalName: "S02E04.The.Boys.1080p",
				CleanTitle:   "The Boys",
				MediaType:    MediaTypeTV,
				TitleLang:    TitleLangLatin,
			},
		},
		{
			name:  "Захист від go-ptn: рік через 'O' та інше сміття",
			input: "Avatar.2O09.BDRip.2000Mb.mkv", // 'O' замість нуля, 2000 як розмір
			expected: ParsedFile{
				OriginalName: "Avatar.2O09.BDRip.2000Mb",
				CleanTitle:   "Avatar",
				Year:         2009, // Має витягнути 2009, а не 2000
				MediaType:    MediaTypeMovie,
				TitleLang:    TitleLangLatin,
			},
		},
		{
			name:  "Кирилиця та IMDB ID",
			input: "C:/Downloads/tt1375666/Початок.2010.mkv",
			expected: ParsedFile{
				OriginalName: "Початок.2010",
				CleanTitle:   "Початок",
				Year:         2010,
				MediaType:    MediaTypeMovie,
				TitleLang:    TitleLangCyrillic,
				IMDBID:       "tt1375666",
				ParentDir:    "tt1375666",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseFilename(tt.input)

			// Ігноруємо ParentDir для тестів де він не заданий явно
			if tt.expected.ParentDir == "" {
				got.ParentDir = ""
			}

			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("\nОтримано: %+v\nОчікувано: %+v", got, tt.expected)
			}
		})
	}
}

func TestResolveHomoglyphs(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Рік і Морті", "Рік і Морті"},            // Чиста кирилиця
		{"Rick and Morty", "Rick and Morty"},      // Чиста латиниця
		{"Сhеrnobyl", "Chernobyl"},                // Латиниця з вкрапленнями кириличних 'С' та 'е'
		{"Дoм кoнца времён", "Дом конца времён"},  // Кирилиця з латинськими 'o'
	}

	for _, tt := range tests {
		if got := resolveHomoglyphs(tt.input); got != tt.expected {
			t.Errorf("Для %q очікувано %q, отримано %q", tt.input, tt.expected, got)
		}
	}
}
