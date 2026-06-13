package utils

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

type contextKey string

const traceIDKey contextKey = "trace_id"

// safeWriter обгортає io.Writer і глушить помилки запису.
// Якщо файл логів заблокується, це не заблокує вивід у консоль і роботу програми.
type safeWriter struct {
	w io.Writer
}

func (sw safeWriter) Write(p []byte) (n int, err error) {
	// Write errors are intentionally ignored to prevent log recursion.
	n, _ = sw.w.Write(p) // Ігноруємо помилку запису
	return n, nil        // Завжди повертаємо nil, щоб MultiWriter не переривався
}

var logFile *os.File

// InitLogger ініціалізує структуроване логування (slog) з виводом у консоль та файл.
func InitLogger() {
	if err := os.MkdirAll("logs", 0755); err != nil {
		slog.Error("failed_to_create_logs_dir", slog.Any("error", err))
	}

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

func CloseLogger() {
	slog.Info("app_closed")
	if logFile != nil {
		logFile.Sync()
		logFile.Close()
	}
}
