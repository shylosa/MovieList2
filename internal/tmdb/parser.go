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

// reFrontEpisode — детектор SххEхх на ПОЧАТКУ рядка (до назви фільму), що ламає go-ptn.
var reFrontEpisode = regexp.MustCompile(`(?i)^(s\d{2}e\d{2}[\.\.\-\s_]+)(.*)`)

// reIMDB — детектор IMDb ID (tt1234567)
var reIMDB = regexp.MustCompile(`(?i)tt\d{7,10}`)

// Оновлюємо ParseFilename, щоб він приймав ПОВНИЙ шлях
func ParseFilename(fullPath string) ParsedFile {
	name := filepath.Base(fullPath)
	ext := filepath.Ext(name)
	name = strings.TrimSuffix(name, ext)

	result := ParsedFile{
		OriginalName: name,
		ParentDir:    filepath.Base(filepath.Dir(fullPath)),
	}

	// Шукаємо IMDb ID у всьому шляху
	if match := reIMDB.FindString(fullPath); match != "" {
		result.IMDBID = strings.ToLower(match)
	}

	// 🛡️ АГРЕСИВНИЙ ПОШУК РОКУ (Рятує від помилок go-ptn)
	// Нормалізуємо одруківки: латинська та кирилична "О" замість нуля (напр. 2O15 -> 2015)
	searchName := strings.ReplaceAll(name, "O", "0")
	searchName = strings.ReplaceAll(searchName, "О", "0") // кирилична
	searchName = strings.ReplaceAll(searchName, "o", "0")
	searchName = strings.ReplaceAll(searchName, "о", "0")

	manualYear := 0
	maxAllowedYear := time.Now().Year() + 1 // +1 для ранніх WEB-релізів/анонсів

	matches := reYear.FindAllString(searchName, -1)
	for _, m := range matches {
		y := mustAtoi(m)
		// Беремо ПЕРШИЙ знайдений валідний рік. Це вирішує проблему, коли 
		// після року йде бітрейт (напр. "2017 BDRip 2000")
		if y > 1900 && y <= maxAllowedYear {
			if manualYear == 0 {
				manualYear = y
			}
		}
	}

	// 🟡 НОРМАЛІЗАЦІЯ ПРЕФІКСУ: go-ptn ламається, якщо S07E05 стоїть ДО назви.
	// Зсуваємо маркер епізоду в кінець рядка (звичний формат для ptn).
	if m := reFrontEpisode.FindStringSubmatch(name); len(m) == 3 {
		name = m[2] + " " + strings.TrimRight(m[1], ".-_ ")
	}

	// Детектуємо S07 без епізоду ДО парсингу go-ptn
	isSeasonOnly := reSeason.MatchString(name)

	info, err := ptn.Parse(name + ext)
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
		// ЗАХИСТ: go-ptn часто плутає цифру 2000 в кінці (бітрейт/розмір) з роком.
		// Якщо ми знайшли інший валідний рік раніше — довіряємо нашому.
		if info.Year == 2000 && manualYear > 0 && manualYear != 2000 {
			result.Year = manualYear
		}
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
