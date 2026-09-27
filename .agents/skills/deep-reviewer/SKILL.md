---
name: deep-reviewer
description: Run a deep-dive architecture, edge case, security, and test audit for MovieList changes.
---

# MovieList Deep Review Workflow

This skill executes a comprehensive read-only audit of MovieList changes focusing on architecture, edge cases, test coverage, and security.

## Review Steps

1. **Strict Read-Only Execution:**
   - Never edit files, stage changes, commit, or run mutating commands.
   - Run read-only commands: `git status`, `git diff`, `git log`, `go test ./...`.

2. **Inspect Changes & Scope:**
   - Determine scope (staged, unstaged, branch diff, or latest commits).
   - Review affected files against [AGENTS.md](file:///d:/movielist2/movielist-app/AGENTS.md).

3. **Audit Dimensions:**
   - **Architecture:** Separation of concerns, SQLite connection pool singleton (`SetMaxOpenConns(1)`), `SaveMoviesBatch()` transactions, immutable `filename` primary key.
   - **Edge Cases & Lifecycle:** `ctx.Err()` cancellation checks, timeout handling, quota backoff, `a.wg` lifecycle safety during shutdown.
   - **Recognition Invariants:** `TmdbID > 0` validation, typed endpoints (`/search/movie`, `/search/tv`), UA title protection.
   - **Security:** Secret redaction (API keys), safe DOM updates (no raw HTML injection).
   - **Testing:** Automated test coverage for new error and fallback paths.

4. **Output Report (in Ukrainian):**
   - **Архітектурний підсумок**
   - **Знахідки за пріоритетом (P1, P2, P3):** Файл/рядок, Категорія, Сценарій відмови, Вплив, Рекомендація щодо виправлення.
   - **Прогалини у верифікації**
