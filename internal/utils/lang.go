package utils

import (
	"strings"
	"unicode"
)

// cyrToLatDoubles — подвоєні кириличні приголосні (BGN/PCGN: зберігаються у латиниці).
// Обробляються ПЕРЕД одинарними, щоб "тт" не розпався на два "t" через інший механізм.
var cyrToLatDoubles = strings.NewReplacer(
	"тт", "tt", "ТТ", "TT", "Тт", "Tt", "тТ", "tT",
	"лл", "ll", "ЛЛ", "LL", "Лл", "Ll", "лЛ", "lL",
	"нн", "nn", "НН", "NN", "Нн", "Nn", "нН", "nN",
	"рр", "rr", "РР", "RR", "Рр", "Rr", "рР", "rR",
	"сс", "ss", "СС", "SS", "Сс", "Ss", "сС", "sS",
	"мм", "mm", "ММ", "MM", "Мм", "Mm", "мМ", "mM",
	"кк", "kk", "КК", "KK", "Кк", "Kk", "кК", "kK",
	"пп", "pp", "ПП", "PP", "Пп", "Pp", "пП", "pP",
	"бб", "bb", "ББ", "BB", "Бб", "Bb", "бБ", "bB",
	"дд", "dd", "ДД", "DD", "Дд", "Dd", "дД", "dD",
	"зз", "zz", "ЗЗ", "ZZ", "Зз", "Zz", "зЗ", "zZ",
	"фф", "ff", "ФФ", "FF", "Фф", "Ff", "фФ", "fF",
	"жж", "zhzh", "ЖЖ", "ZHZH", "Жж", "Zhzh", "жЖ", "zhZh",
	"шш", "shsh", "ШШ", "SHSH", "Шш", "Shsh", "шШ", "shSh",
	"чч", "chch", "ЧЧ", "CHCH", "Чч", "Chch", "чЧ", "chCh",
)

// cyrToLatSingles — одинарні кириличні символи → латиниця.
var cyrToLatSingles = strings.NewReplacer(
	"а", "a", "А", "A",
	"б", "b", "Б", "B",
	"в", "v", "В", "V",
	"г", "g", "Г", "G",
	"д", "d", "Д", "D",
	"е", "e", "Е", "E",
	"ё", "yo", "Ё", "Yo",
	"ж", "zh", "Ж", "Zh",
	"з", "z", "З", "Z",
	"и", "i", "И", "I",
	"й", "y", "Й", "Y",
	"к", "k", "К", "K",
	"л", "l", "Л", "L",
	"м", "m", "М", "M",
	"н", "n", "Н", "N",
	"о", "o", "О", "O",
	"п", "p", "П", "P",
	"р", "r", "Р", "R",
	"с", "s", "С", "S",
	"т", "t", "Т", "T",
	"у", "u", "У", "U",
	"ф", "f", "Ф", "F",
	"х", "kh", "Х", "Kh",
	"ц", "ts", "Ц", "Ts",
	"ч", "ch", "Ч", "Ch",
	"ш", "sh", "Ш", "Sh",
	"щ", "shch", "Щ", "Shch",
	"ъ", "", "Ъ", "",
	"ы", "y", "Ы", "Y",
	"ь", "", "Ь", "",
	"э", "e", "Э", "E",
	"ю", "yu", "Ю", "Yu",
	"я", "ya", "Я", "Ya",
	"і", "i", "І", "I",
	"ї", "i", "Ї", "I",
	"є", "e", "Є", "E",
	"ґ", "g", "Ґ", "G",
)

// CyrillicToLatin transliterates Cyrillic text to Latin for TMDB en-US search.
// Double consonants are preserved per BGN/PCGN (e.g. "Скарпетта" → "Skarpetta").
func CyrillicToLatin(s string) string {
	if !HasCyrillic(s) {
		return s
	}
	s = cyrToLatDoubles.Replace(s)
	return cyrToLatSingles.Replace(s)
}

// IsCyrillic returns true if the rune belongs to the Cyrillic script.
func IsCyrillic(r rune) bool {
	return unicode.Is(unicode.Cyrillic, r)
}

// HasCyrillic returns true if the string contains at least one Cyrillic rune.
func HasCyrillic(s string) bool {
	for _, r := range s {
		if IsCyrillic(r) {
			return true
		}
	}
	return false
}

// IsGoodUkrainian returns true if the string looks like valid Ukrainian text.
// It requires Cyrillic script, at least one Ukrainian-specific character, and no
// strong Russian-only markers.
func IsGoodUkrainian(s string) bool {
	if s == "" {
		return false
	}

	foundCyrillic := false
	foundRussianLetter := false
	foundUkrainianLetter := false

	for _, r := range s {
		if IsCyrillic(r) {
			foundCyrillic = true
		}
		switch r {
		case 'ы', 'э', 'ъ', 'ё', 'Ы', 'Э', 'Ъ', 'Ё':
			foundRussianLetter = true
		case 'і', 'ї', 'є', 'ґ', 'І', 'Ї', 'Є', 'Ґ':
			foundUkrainianLetter = true
		}
	}

	return foundCyrillic && !foundRussianLetter && foundUkrainianLetter
}
