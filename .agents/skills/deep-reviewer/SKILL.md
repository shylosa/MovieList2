---
name: deep-reviewer
description: Run a deep-dive architecture, edge case, security, and test audit for MovieList changes.
---

# MovieList Deep Review Workflow

This skill executes a comprehensive read-only audit of MovieList changes focusing on architecture, edge cases, test coverage, and security.

Invoke this review only when the user explicitly requests a review. Updating agent instructions is not an instruction to run an audit.

## Current Feature Contracts

Check current MovieList contracts against AGENTS.md:
- internal/version/VERSION is the only editable release version; build.ps1 synchronizes Wails/npm/HTML metadata. Generated version copies are not release sources.
- The .env editor accesses only Config.EnvPath, validates syntax, rejects stale revisions and atomically saves UTF-8/LF. Never log config contents or raw parser errors. Saving requires restart to affect active configuration; persisted folder/model choices retain precedence.
- Independent scan roots preserve legacy filename keys and stable source namespaces. Empty/unavailable sources abort before cleanup; exclusions skip the intended subtree.
- Model catalogs are explicit requests; selections autosave and are captured per operation, including background localization. Keep Groq credentials/endpoints separate from xAI. Require explicit valid confidence, model-specific reasoning parameters, sequential fallback and shared rate limiting. Output budgets do not bypass account input limits.
- Scan starts only in Library. About is a modal closed by button, backdrop or Escape, returning focus without changing the active panel.

Use isolated API checks by default. Do not enable live test flags without explicit user authorization for external requests and quota usage. Synthetic live checks do not establish final TMDB identities, full production scan quality or Wails UI behavior.

## Review Steps

1. **Strict Read-Only Execution:**
   - Never edit files, stage changes, commit, or run mutating commands.
   - Run read-only commands: `git status`, `git diff`, `git log`, `go test ./...`.

2. **Inspect Changes & Scope:**
   - Determine scope (staged, unstaged, branch diff, or latest commits).
   - Review affected files against [AGENTS.md](../../../AGENTS.md).

3. **Audit Dimensions:**
   - **Architecture:** Separation of concerns, SQLite connection pool singleton (`SetMaxOpenConns(1)`), `SaveMoviesBatch()` transactions, immutable `filename` primary key.
   - **Edge Cases & Lifecycle:** `ctx.Err()` cancellation checks, timeout handling, bounded quota retries, `a.wg` lifecycle safety during shutdown.
   - **Recognition Invariants:** `TmdbID > 0` validation, typed endpoints (`/search/movie`, `/search/tv`), UA title protection.
   - **Security:** Secret redaction (API keys), safe DOM updates (no raw HTML injection).
   - **Testing:** Automated test coverage for new error and fallback paths.

4. **Output Report (in Ukrainian):**
   - **Архітектурний підсумок**
   - **Знахідки за пріоритетом (P1, P2, P3):** Файл/рядок, Категорія, Сценарій відмови, Вплив, Рекомендація щодо виправлення.
   - **Прогалини у верифікації**
