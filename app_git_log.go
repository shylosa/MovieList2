package main

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"movielist-app/internal/utils"
)

var (
	gitDiagnosticURL        = regexp.MustCompile(`(?i)\b(?:https?|ssh|git|file)://[^\s<>"']+|\bgit@[^\s<>"']+`)
	gitDiagnosticToken      = regexp.MustCompile(`\b(?:github_pat_|gh[pousr]_)[A-Za-z0-9_]+`)
	gitDiagnosticCredential = regexp.MustCompile(`(?im)(authorization\s*:[^\r\n]*|(?:password|token|access_token)\s*[=:]\s*[^\s"']+)`)
	gitDiagnosticPath       = regexp.MustCompile(`[A-Za-z]:[\\/][^\r\n"'<>]*`)
)

func safeGitDiagnostic(value string) string {
	value = gitDiagnosticURL.ReplaceAllString(value, "[remote]")
	value = gitDiagnosticToken.ReplaceAllString(value, "[credential]")
	value = gitDiagnosticCredential.ReplaceAllString(value, "[credential]")
	value = gitDiagnosticPath.ReplaceAllString(value, "[path]")
	value = strings.TrimSpace(value)
	const limit = 8192
	if len(value) > limit {
		value = strings.ToValidUTF8(value[:limit], "") + " [truncated]"
	}
	return value
}

func (a *App) logGitFailure(command string, output []byte, err error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	diagnostic := safeGitDiagnostic(string(output))
	logger := utils.LoggerWithTrace(ctx)
	attrs := []any{slog.String("command", command), slog.String("output", diagnostic),
		slog.String("error", safeGitDiagnostic(err.Error()))}
	// Git exits nonzero for an unchanged showcase; the following push still runs.
	if command == "commit" && (strings.Contains(diagnostic, "nothing to commit") || strings.Contains(diagnostic, "nothing added to commit")) {
		logger.Info("git_commit_no_changes", attrs...)
		return
	}
	logger.Error("git_command_failed", attrs...)
}
