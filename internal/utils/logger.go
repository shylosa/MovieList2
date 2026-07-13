package utils

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

type contextKey string

const traceIDKey contextKey = "trace_id"

// safeWriter обгортає io.Writer і глушить помилки запису.
// Завжди повертає len(p), nil, щоб io.MultiWriter не переривав обхід і продовжував
// записувати в решту writers. Це критично для production-білдів Wails (-H windowsgui),
// де os.Stdout є недійсним handle і будь-який Write на нього повертає помилку.
type safeWriter struct {
	w io.Writer
}

func (sw safeWriter) Write(p []byte) (int, error) {
	sw.w.Write(p) //nolint:errcheck — помилки ігноруємо навмисно
	return len(p), nil // Завжди репортуємо повний запис, щоб MultiWriter не обривався
}

var (
	logFile  *os.File
	syncStop chan struct{} // закриття зупиняє фонову горутину periodicSync
)

// InitLogger ініціалізує структуроване логування (slog) з виводом у консоль та файл.
func InitLogger() {
	// Визначаємо базовий каталог відносно .exe, як це робить config.Load().
	// Це гарантує коректний шлях незалежно від CWD (подвійний клік, ярлик тощо).
	logsDir := "logs"
	if exePath, err := os.Executable(); err == nil {
		logsDir = filepath.Join(filepath.Dir(exePath), "logs")
	}

	if err := os.MkdirAll(logsDir, 0755); err != nil {
		slog.Error("failed_to_create_logs_dir", slog.Any("error", err))
	}

	// Відкриваємо файл. Якщо не вдалося — просто не використовуємо його, але програма працює
	var err error
	logFile, err = os.OpenFile(filepath.Join(logsDir, "app.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)

	var writer io.Writer = os.Stdout
	if err == nil {
		// Огортаємо обидва writer-и в safeWriter.
		// os.Stdout у production Wails-білді (-H windowsgui) є недійсним handle —
		// без safeWriter MultiWriter обривається на ньому і логи не доходять до файлу.
		writer = io.MultiWriter(safeWriter{w: os.Stdout}, safeWriter{w: logFile})

		// Запускаємо фоновий Sync щоб NTFS оновлював метадані директорії в реальному часі.
		// Без цього Windows Explorer показує розмір файлу 0 кб поки програма працює,
		// бо $FILE_NAME атрибут оновлюється лише при FlushFileBuffers або CloseHandle.
		syncStop = make(chan struct{})
		go periodicSync(logFile, 3*time.Second, syncStop)
	}

	opts := &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				t := a.Value.Time()
				return slog.String(a.Key, t.Format("2006-01-02T15:04:05.000"))
			}
			return a
		},
	}

	handler := slog.NewJSONHandler(writer, opts)
	slog.SetDefault(slog.New(handler))
}

// ContextWithTrace додає trace_id до контексту
func ContextWithTrace(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// EnsureTrace checks if context contains trace_id. If not, generates one.
func EnsureTrace(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if v, ok := ctx.Value(traceIDKey).(string); ok && v != "" {
		return ctx
	}
	return context.WithValue(ctx, traceIDKey, uuid.New().String()[:8])
}

// LoggerWithTrace дістає trace_id з контексту і додає його до логера
func LoggerWithTrace(ctx context.Context) *slog.Logger {
	traceID, ok := ctx.Value(traceIDKey).(string)
	if !ok || traceID == "" {
		traceID = "unknown"
	}
	return slog.Default().With(slog.String("trace_id", traceID))
}

// periodicSync викликає file.Sync() кожні interval секунд.
// Змушує NTFS оновлювати $FILE_NAME атрибут, тому Explorer показує актуальний розмір файлу.
func periodicSync(f *os.File, interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			f.Sync()
		case <-stop:
			return
		}
	}
}

// CloseLogger зупиняє фонову синхронізацію, синхронізує та закриває файл логів.
// app_closed логується в app.shutdown() — реальному Wails OnShutdown хуку.
func CloseLogger() {
	if syncStop != nil {
		close(syncStop)
		syncStop = nil
	}
	if logFile != nil {
		logFile.Sync()
		logFile.Close()
	}
}
