---
name: movielist-log-analysis
description: Analyze MovieList scan or correction logs for recognition results, cancellation, quota failures, and request metrics.
---

# MovieList Log Analysis

Analyze the supplied log or requested local session without changing code, catalog data, or checklist status unless separately requested. Follow [AGENTS.md](../../../AGENTS.md).

## Evidence

Logs are JSONL, normally `logs/app.jsonl` beside the running executable, and append across sessions. Do not assume the repository log is the current production log. Use the supplied path or identify available logs without launching the app.

Start with event counts and operation boundaries, then inspect relevant records only. Use a streaming JSONL reader for large files rather than dumping the whole log. Group by `trace_id`, timestamp, and operation; preserve file and line references. Missing traces and malformed/truncated lines are evidence gaps; do not silently discard them or merge unrelated scans.

Never print keys, authorization values, raw provider payloads, or unredacted URLs/errors. Report any secret's presence and location without reproducing it. Timestamps have no UTC offset; confirm the session timezone before converting them.

## Interpretation

- `scan_completed` is emitted by deferred finalization even on cancellation or earlier failure. Inspect `cancelled`, `scan_cancelled`, and preceding errors before calling a scan successful.
- For complete successful scans, check `processed_total = tmdb_accepted + ai_accepted + unresolved`. `disk_total` is collection size, not the recognition denominator. Interpret incomplete-scan mismatches in their cancellation/error context.
- Distinguish unresolved items from verified but suspicious records; `needs_review` is not an unresolved count. Check `resolved = tmdb_accepted + ai_accepted`.
- Compare TMDB search/details/cache counters, Gemini counters, `groq_calls`, and legacy `grok_calls`. They measure requests, not unique movies or final correctness. Retries alone do not prove final failure.
- Trace suspect filenames through parsing, typed TMDB lookup, folder/transliteration rescue, AI verification, and persistence. Verify event meanings in `app.go`, `internal/tmdb/`, or `internal/ai/` only for involved paths.
- Separate quota/rate limits, account input limits such as `request_too_large`, unavailable models, network errors, and cancellation. Failed attempts followed by successful fallback differ from final unresolved results.

Recognition requires a verified TMDB ID greater than zero. AI-supplied IDs or plausible titles alone do not establish identity. Use final verification/persistence evidence; if absent, say the identity is unconfirmed. Synthetic checks do not establish full-library scan quality.

Give a concise Ukrainian report: session scope, counters, actionable anomalies with trace/file/line evidence, and uncertainty. Separate observations from hypotheses. Do not complete manual production checks from partial logs or start live API requests to fill evidence gaps.
