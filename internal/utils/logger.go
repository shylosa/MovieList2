package utils

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type DailyLogger struct {
	mu          sync.Mutex
	logFile     *os.File
	currentDate string
	initialized bool // Прапорець для відстеження ініціалізації
}

func (l *DailyLogger) Write(p []byte) (n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	dateStr := now.Format("2006-01-02") // Для назви файлу (YYYY-MM-DD)
	timeStr := now.Format("2006-01-02 15:04:05") // Для самого логу (ISO)

	// Якщо дата змінилася або файл не відкритий
	if l.currentDate != dateStr || l.logFile == nil {
		if l.logFile != nil {
			l.logFile.Close()
		}

		logsDir := "logs"
		_ = os.MkdirAll(logsDir, 0755)

		logPath := filepath.Join(logsDir, fmt.Sprintf("%s.log", dateStr))
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
            // Якщо не вийшло створити файл, пишемо в консоль
			fmt.Printf("%s %s", timeStr, string(p))
			return len(p), nil
		}
		l.logFile = f
		l.currentDate = dateStr
	}

	// Формуємо фінальний рядок: наш ISO-час + стандартний лог (де вже є file.go:line)
	finalLog := []byte(fmt.Sprintf("%s %s", timeStr, string(p)))

	// Пишемо у файл і в консоль
	mw := io.MultiWriter(l.logFile, os.Stdout)
	_, err = mw.Write(finalLog)

	// ВАЖЛИВО: повертаємо оригінальну довжину p, щоб стандартний пакет log не сварився
	return len(p), err
}

var GlobalLogger = &DailyLogger{}

func InitLogger() {
	// Встановлюємо логер ТІЛЬКИ ОДИН РАЗ
	if !GlobalLogger.initialized {
		log.SetOutput(GlobalLogger)

		// ВИМИКАЄМО Ldate та Ltime, залишаємо ТІЛЬКИ Lshortfile (назву файлу і рядок)
		log.SetFlags(log.Lshortfile)

		GlobalLogger.initialized = true
	}
}

func CloseLogger() {
	GlobalLogger.mu.Lock()
	defer GlobalLogger.mu.Unlock()
	if GlobalLogger.logFile != nil {
		// Тут також замінив слеші на дефіси
		fmt.Fprintln(GlobalLogger.logFile, time.Now().Format("2006-01-02 15:04:05"), "🛑 Програму закрито.")
		GlobalLogger.logFile.Close()
		GlobalLogger.logFile = nil
	}
}
