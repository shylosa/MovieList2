package utils

import (
	"context"
	"log/slog"
	"os"
)

type contextKey string

const traceIDKey contextKey = "trace_id"

// InitStructuredLogger створює JSON-логер, який пише у файл logs/app.jsonl
func InitStructuredLogger() {
	opts := &slog.HandlerOptions{
		Level: slog.LevelDebug, // Або який там у тебе рівень
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Перехоплюємо поле з часом
			if a.Key == slog.TimeKey {
				t := a.Value.Time()

				return slog.String(a.Key, t.Format("2006-01-02T15:04:05.000"))
			}
			return a
		},
	}

	// Застосовуємо опції до JSON хендлера
	handler := slog.NewJSONHandler(os.Stdout, opts)

	// Встановлюємо як логер за замовчуванням
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

// Залишаємо старі методи для сумісності, поки не переведемо весь проект на slog
func InitLogger() {
	// Порожній, бо slog ініціалізується через InitStructuredLogger
}

func CloseLogger() {
	// Можна додати фінальний лог через slog
	slog.Info("app_closed")
}
