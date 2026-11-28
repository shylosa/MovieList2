package tmdb

import (
	"encoding/json"
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
		// Cases for naked language counters (BUG-03)
		{
			name:  "3xRus without brackets should be removed",
			input: "D:/Movies/Fracture.2007.1080p.BluRay.3xRus.Eng.HDCLUB.720.mp4",
			expected: ParsedFile{
				OriginalName: "Fracture.2007.1080p.BluRay.3xRus.Eng.HDCLUB.720",
				CleanTitle:   "Fracture",
				Year:         2007,
				MediaType:    MediaTypeMovie,
				TitleLang:    TitleLangLatin,
			},
		},
		{
			name:  "2xUkr without brackets should be removed",
			input: "D:/Movies/Film.Title.2xUkr.Eng.2021.mkv",
			expected: ParsedFile{
				OriginalName: "Film.Title.2xUkr.Eng.2021",
				CleanTitle:   "Film Title",
				Year:         2021,
				MediaType:    MediaTypeMovie,
				TitleLang:    TitleLangLatin,
			},
		},
		{
			name:  "Open Matte release marker should be removed",
			input: "Fracture.Open.Matte.2007.WEB-DLRip.by.Seven.avi",
			expected: ParsedFile{
				OriginalName: "Fracture.Open.Matte.2007.WEB-DLRip.by.Seven",
				CleanTitle:   "Fracture",
				Year:         2007,
				MediaType:    MediaTypeMovie,
				TitleLang:    TitleLangLatin,
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

func TestR13AParseFilenameExtensions(t *testing.T) {
	tests := []struct {
		input, original, title string
		year                   int
		mediaType              MediaType
		lang                   TitleLanguage
		parent, imdb           string
	}{
		{"Охрана звёзд.mkv", "Охрана звёзд", "Охрана звёзд", 0, MediaTypeMovie, TitleLangCyrillic, ".", ""},
		{"Охрана звёзд.2026.mkv", "Охрана звёзд.2026", "Охрана звёзд", 2026, MediaTypeMovie, TitleLangCyrillic, ".", ""},
		{"Охрана звёзд.1080p.mkv", "Охрана звёзд.1080p", "Охрана звёзд", 0, MediaTypeMovie, TitleLangCyrillic, ".", ""},
		{"Охрана звёзд.2026.1080p.mkv", "Охрана звёзд.2026.1080p", "Охрана звёзд", 2026, MediaTypeMovie, TitleLangCyrillic, ".", ""},
		{"Film.mp4", "Film", "Film", 0, MediaTypeMovie, TitleLangLatin, ".", ""},
		{"Film.2024.avi", "Film.2024", "Film", 2024, MediaTypeMovie, TitleLangLatin, ".", ""},
		{"Film.mov", "Film", "Film", 0, MediaTypeMovie, TitleLangLatin, ".", ""},
		{"Film.MKV", "Film", "Film", 0, MediaTypeMovie, TitleLangLatin, ".", ""},
		{"Film.Mp4", "Film", "Film", 0, MediaTypeMovie, TitleLangLatin, ".", ""},
		{"Mr. & Mrs. Smith.mkv", "Mr. & Mrs. Smith", "Mr. & Mrs. Smith", 0, MediaTypeMovie, TitleLangLatin, ".", ""},
		{"Dr. Strangelove.1964.mkv", "Dr. Strangelove.1964", "Dr. Strangelove", 1964, MediaTypeMovie, TitleLangLatin, ".", ""},
		{"WALL.E.2008.mkv", "WALL.E.2008", "WALL E", 2008, MediaTypeMovie, TitleLangLatin, ".", ""},
		{"Назва.2026", "Назва.2026", "Назва", 2026, MediaTypeMovie, TitleLangCyrillic, ".", ""},
		{"Назва", "Назва", "Назва", 0, MediaTypeMovie, TitleLangCyrillic, ".", ""},
		{"Mr. & Mrs. Smith", "Mr. & Mrs. Smith", "Mr. & Mrs. Smith", 0, MediaTypeMovie, TitleLangLatin, ".", ""},
		{"Dr. Strangelove", "Dr. Strangelove", "Dr. Strangelove", 0, MediaTypeMovie, TitleLangLatin, ".", ""},
		{"WALL.E", "WALL.E", "WALL E", 0, MediaTypeMovie, TitleLangLatin, ".", ""},
		{"Movies/Назва.2026", "Назва.2026", "Назва", 2026, MediaTypeMovie, TitleLangCyrillic, "Movies", ""},
		{"Shows/S01E01.The.Boys.mkv", "S01E01.The.Boys", "The Boys", 0, MediaTypeTV, TitleLangLatin, "Shows", ""},
		{"Shows/The.Rookie.S07.mkv", "The.Rookie.S07", "The Rookie", 0, MediaTypeTV, TitleLangLatin, "Shows", ""},
		{"Movies/TT1375666/Початок.2010.mkv", "Початок.2010", "Початок", 2010, MediaTypeMovie, TitleLangCyrillic, "TT1375666", "tt1375666"},
		{"Film.Title.2xUkr.[2xUkr,Eng].2021.mkv", "Film.Title.2xUkr.[2xUkr,Eng].2021", "Film Title", 2021, MediaTypeMovie, TitleLangLatin, ".", ""},
		{"Fracture.Open.Matte.2007.WEB-DLRip.by.Seven.avi", "Fracture.Open.Matte.2007.WEB-DLRip.by.Seven", "Fracture", 2007, MediaTypeMovie, TitleLangLatin, ".", ""},
		{"Once.Upon.a.Time", "Once.Upon.a.Time", "Once Upon a Time", 0, MediaTypeMovie, TitleLangLatin, ".", ""},
		{"Movies/Далека планета.mov", "Далека планета", "Далека планета", 0, MediaTypeMovie, TitleLangCyrillic, "Movies", ""},
		{"Назва.2О26", "Назва.2О26", "Назва", 2026, MediaTypeMovie, TitleLangCyrillic, ".", ""},
		{"Shows/The.Rookie.S07", "The.Rookie.S07", "The Rookie", 0, MediaTypeTV, TitleLangLatin, "Shows", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			want := ParsedFile{
				OriginalName: tt.original, CleanTitle: tt.title, Year: tt.year,
				MediaType: tt.mediaType, TitleLang: tt.lang, ParentDir: tt.parent, IMDBID: tt.imdb,
			}
			got := ParseFilename(tt.input)
			observation, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("R13A parse %s", observation)
			if got != want {
				t.Errorf("ParseFilename(%q) = %+v; want %+v", tt.input, got, want)
			}
		})
	}
}

func TestResolveHomoglyphs(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Рік і Морті", "Рік і Морті"},           // Чиста кирилиця
		{"Rick and Morty", "Rick and Morty"},     // Чиста латиниця
		{"Сhеrnobyl", "Chernobyl"},               // Латиниця з вкрапленнями кириличних 'С' та 'е'
		{"Дoм кoнца времён", "Дом конца времён"}, // Кирилиця з латинськими 'o'
	}

	for _, tt := range tests {
		if got := resolveHomoglyphs(tt.input); got != tt.expected {
			t.Errorf("Для %q очікувано %q, отримано %q", tt.input, tt.expected, got)
		}
	}
}

func TestNormalizeYearLikeTokens(t *testing.T) {
	for _, input := range []string{"Iz.Tmy.2O16.mkv", "Iz.Tmy.2o16.mkv", "Iz.Tmy.2О16.mkv", "Iz.Tmy.2о16.mkv"} {
		parsed := ParseFilename(input)
		if parsed.Year != 2016 {
			t.Errorf("ParseFilename(%q).Year = %d; want 2016", input, parsed.Year)
		}
	}

	for _, word := range []string{"Room", "Doctor", "Oppenheimer"} {
		if got := normalizeYearLikeTokens(word); got != word {
			t.Errorf("normalizeYearLikeTokens(%q) = %q; want unchanged", word, got)
		}
	}
	if got := ParseFilename("Film.2O99.mkv").Year; got != 0 {
		t.Errorf("invalid future year parsed as %d; want 0", got)
	}
}
