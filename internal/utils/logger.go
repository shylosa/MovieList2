package utils

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

type contextKey string

const traceIDKey contextKey = "trace_id"

// safeWriter обгортає io.Writer і глушить помилки запису.
// Якщо файл логів заблокується, це не заблокує вивід у консоль і роботу програми.
type safeWriter struct {
	w io.Writer
}

func (sw safeWriter) Write(p []byte) (n int, err error) {
	n, _ = sw.w.Write(p) // Ігноруємо помилку запису
	return n, nil        // Завжди повертаємо nil, щоб MultiWriter не переривався
}

var logFile *os.File

// InitLogger ініціалізує структуроване логування (slog) з виводом у консоль та файл.
func InitLogger() {
	os.MkdirAll("logs", 0755)

	// Відкриваємо файл. Якщо не вдалося — просто не використовуємо його, але програма працює
	var err error
	logFile, err = os.OpenFile(filepath.Join("logs", "app.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)

	var writer io.Writer = os.Stdout
	if err == nil {
		// Використовуємо MultiWriter + наш safeWriter для одночасного запису
		writer = io.MultiWriter(os.Stdout, safeWriter{w: logFile})
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

// LoggerWithTrace дістає trace_id з контексту і додає його до логера
func LoggerWithTrace(ctx context.Context) *slog.Logger {
	traceID, ok := ctx.Value(traceIDKey).(string)
	if !ok {
		traceID = "unknown"
	}
	return slog.Default().With(slog.String("trace_id", traceID))
}

func CloseLogger() {
	slog.Info("app_closed")
	if logFile != nil {
		logFile.Sync()
		logFile.Close()
	}
}
