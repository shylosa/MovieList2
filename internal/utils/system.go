package utils

import (
	"log"
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
			log.Printf("⚠️ Не вдалося створити папку logs: %v", err)
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
		log.Printf("⚠️ Не вдалося відкрити папку: %v", err)
	}
}
