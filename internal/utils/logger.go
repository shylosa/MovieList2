package utils

import (
	"context"
	"log/slog"
	"os"
)

type contextKey string

const traceIDKey contextKey = "trace_id"

// InitStructuredLogger створює JSON-логер, який пише у файл logs/app.jsonl
func InitStructuredLogger() *os.File {
	os.MkdirAll("logs", 0755)
	logFile, err := os.OpenFile("logs/app.jsonl", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		// Якщо не вдалося відкрити файл, slog буде писати в stderr за замовчуванням
		return nil
	}

	handler := slog.NewJSONHandler(logFile, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	logger := slog.New(handler)
	slog.SetDefault(logger) // Тепер всі slog.Info() будуть писати сюди

	return logFile
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

// Залишаємо старі методи для сумісності, поки не переведемо весь проект на slog
func InitLogger() {
	// Порожній, бо slog ініціалізується через InitStructuredLogger
}

func CloseLogger() {
	// Можна додати фінальний лог через slog
	slog.Info("app_closed")
}
