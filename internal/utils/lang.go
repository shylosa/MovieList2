package utils

import "unicode"

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
