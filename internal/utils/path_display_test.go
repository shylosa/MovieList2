package utils

import (
	"testing"
)

func TestDisplayFileLabel(t *testing.T) {
	tests := []struct {
		name         string
		relativePath string
		expected     string
	}{
		{
			name:         "Фільм у каталозі — повертає каталог",
			relativePath: "Banshi.Inisherina.2022.WEB-DLRip/movie.mkv",
			expected:     "Banshi.Inisherina.2022.WEB-DLRip",
		},
		{
			name:         "Серіал у каталозі — повертає каталог",
			relativePath: "How.to.Get.to.Heaven.From.Belfast.S01.2026.NF.WEB-DLRip-AVC.x264.seleZen/How.to.Get.to.Heaven.From.Belfast.S01E08.WEB-DLRip-AVC.x264.seleZen.mkv",
			expected:     "How.to.Get.to.Heaven.From.Belfast.S01.2026.NF.WEB-DLRip-AVC.x264.seleZen",
		},
		{
			name:         "Фільм у вкладеному каталозі — повертає перший рівень",
			relativePath: "Movies/2024/Inception.mkv",
			expected:     "Movies",
		},
		{
			name:         "Плоский файл без каталогу — повертає ім'я файлу",
			relativePath: "Inception.mkv",
			expected:     "Inception.mkv",
		},
		{
			name:         "Windows backslash — коректно обробляється",
			relativePath: `Shows\Severance\S02E01.mkv`,
			expected:     "Shows",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := DisplayFileLabel(tt.relativePath)
			if actual != tt.expected {
				t.Errorf("DisplayFileLabel(%q) = %q; expected %q",
					tt.relativePath, actual, tt.expected)
			}
		})
	}
}
