package tmdb

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"

	ptn "github.com/razsteinmetz/go-ptn"
)

// reSeason — детектор серіальних маркерів що go-ptn пропускає:
// S07 (без епізоду), Season 3, сезон
var reSeason = regexp.MustCompile(`(?i)\bS(\d{2})\b(?:E\d{2})?|\bSeason\s*\d+\b|\bсезон\b`)

// ParseFilename — парсить ім'я файлу через go-ptn + власні доповнення.
//
// go-ptn обробляє: рік, кодеки, якість, рілізгрупи, S01E01 формат.
// Ми додаємо: детектор мови, детектор S07 (сезон без епізоду), cleanup title.
func ParseFilename(raw string) ParsedFile {
	name := filepath.Base(raw)
	ext := filepath.Ext(name)
	name = strings.TrimSuffix(name, ext)

	result := ParsedFile{OriginalName: name}

	// 🛡️ АГРЕСИВНИЙ ПОШУК РОКУ (Рятує від помилок go-ptn)
	// Шукає будь-які числа від 1900 до 2099, відділені межами слова
	reYear := regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	manualYear := 0
	maxAllowedYear := time.Now().Year() + 1 // +1 для ранніх WEB-релізів/анонсів

	matches := reYear.FindAllString(name, -1)
	for _, m := range matches {
		y := mustAtoi(m)
		// Обираємо найбільший рік, який не перевищує ліміт
		if y > manualYear && y <= maxAllowedYear {
			manualYear = y
		}
	}

	// Детектуємо S07 без епізоду ДО парсингу go-ptn
	// (go-ptn розпізнає S01E01 але ігнорує S01 без E)
	isSeasonOnly := reSeason.MatchString(name)

	info, err := ptn.Parse(raw)
	if err != nil || info == nil {
		result.CleanTitle = cleanFallback(name)
		result.MediaType = MediaTypeMovie
		result.Year = manualYear // 👈 Беремо знайдений вручну рік, якщо go-ptn впав
		result.TitleLang = detectLanguage(result.CleanTitle)
		return result
	}

	// MediaType: серіал якщо go-ptn знайшов Season/Episode або ми знайшли S07
	if !info.IsMovie || info.Season > 0 || isSeasonOnly {
		result.MediaType = MediaTypeTV
	} else {
		result.MediaType = MediaTypeMovie
	}

	// 🛡️ Встановлюємо рік: пріоритет за go-ptn, якщо він не знайшов — беремо наш ручний
	if info.Year != 0 {
		result.Year = info.Year
	} else {
		result.Year = manualYear
	}

	title := info.Title

	// go-ptn залишає серіальний маркер у title якщо немає епізоду (напр. "The Rookie S07 WEB")
	if isSeasonOnly {
		title = reSeason.ReplaceAllString(title, "")
	}

	title = cleanResidual(title)
	result.CleanTitle = strings.TrimSpace(title)
	result.TitleLang = detectLanguage(result.CleanTitle)

	return result
}

// --- ОПТИМІЗАЦІЯ: Регулярки скомпільовані один раз при запуску ---
var (
	reResidual = regexp.MustCompile(`(?i)\b(ELEKTRI4KA|UNIONGANG|NNMClub|MegaPeer|stalkerok|new-team|HELLYWOOD|MIXTV|HRIME|HDClub|HDCLUB|LineFilm|grab777|vitolinform|ivanes|seleZen|JNS82|Dalemake|R\.G\.Resident|lexx256|Jaskier|Hurtom)`)
	reLang     = regexp.MustCompile(`(?i)\b(Ukr|Eng|Rus|RUS|DUB|VO|MVO|LF|WEB)\b`)
	reTrail    = regexp.MustCompile(`[\s\-_\[(\.,•:()\]]+$`)
	reSpace    = regexp.MustCompile(`\s{2,}`)
)

// cleanResidual — видаляє хвости, які go-ptn не знає
func cleanResidual(s string) string {
	s = reResidual.ReplaceAllString(s, "")
	s = reLang.ReplaceAllString(s, "")
	s = reTrail.ReplaceAllString(s, "")
	s = reSpace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// cleanFallback — мінімальне очищення якщо go-ptn впав
func cleanFallback(name string) string {
	s := regexp.MustCompile(`[._]`).ReplaceAllString(name, " ")
	return strings.TrimSpace(regexp.MustCompile(`\s{2,}`).ReplaceAllString(s, " "))
}

// detectLanguage визначає мову назви (кирилиця або латиниця)
func detectLanguage(s string) TitleLanguage {
	for _, r := range s {
		if isCyrillic(r) {
			return TitleLangCyrillic
		}
	}
	return TitleLangLatin
}

// isCyrillic перевіряє чи є руна кириличною (рос + укр алфавіти)
func isCyrillic(r rune) bool {
	return (r >= 'а' && r <= 'я') ||
		(r >= 'А' && r <= 'Я') ||
		r == 'і' || r == 'І' ||
		r == 'ї' || r == 'Ї' ||
		r == 'є' || r == 'Є' ||
		r == 'ґ' || r == 'Ґ' ||
		r == 'ё' || r == 'Ё'
}
