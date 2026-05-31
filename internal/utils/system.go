package utils

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// OpenLogsFolder створює папку logs (якщо її немає) і відкриває її у Провіднику
func OpenLogsFolder() {
	logsDir := filepath.Join(".", "logs")

	// Створюємо папку, якщо її ще немає
	if _, err := os.Stat(logsDir); os.IsNotExist(err) {
		err := os.Mkdir(logsDir, 0755)
		if err != nil {
			slog.Error("logs_dir_create_failed", slog.Any("error", err))
			return
		}
	}

	// Відкриваємо папку залежно від ОС
	var err error
	switch runtime.GOOS {
	case "windows":
		err = exec.Command("explorer", logsDir).Start()
	case "darwin":
		err = exec.Command("open", logsDir).Start()
	case "linux":
		err = exec.Command("xdg-open", logsDir).Start()
	}

	if err != nil {
		slog.Error("open_logs_folder_failed", slog.Any("error", err))
	}
}
