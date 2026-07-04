package utils

import (
	"path/filepath"
	"strings"
)

// DisplayFileLabel повертає сиру назву каталогу (якщо файл лежить у папці)
// або ім'я файлу (якщо файл у корені media folder).
// Використовується як навігаційна мітка для пошуку файлу на диску.
// relativePath — значення movies.filename (відносний шлях від media folder).
func DisplayFileLabel(relativePath string) string {
	cleanPath := filepath.ToSlash(relativePath)
	parts := strings.SplitN(cleanPath, "/", 2)
	if len(parts) == 2 {
		return parts[0] // є каталог — повертаємо сиру назву папки
	}
	return filepath.Base(cleanPath) // плоский файл — повертаємо ім'я файлу
}
