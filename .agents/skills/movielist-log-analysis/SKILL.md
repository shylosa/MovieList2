---
name: movielist-log-analysis
description: Analyze MovieList scan or correction logs and assess whether evidence warrants code changes for recognition, localization, cancellation, quota failures, or request usage.
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

## Code-change decision

Every report must end with an explicit assessment of whether code changes are needed: required fixes, recommended improvements, no changes needed, or insufficient evidence. Explain the decision using log evidence and the relevant code or project contract; do not equate an unexpected result, unresolved file, provider failure, or original-title fallback with a code defect.

For actionable changes, provide a short numbered list that the user can approve in one follow-up command. Each item must state the problem and evidence (trace/file/line), priority, concrete implementation scope, expected behavior, and proportional verification. Inspect only the involved code when necessary to make the recommendation actionable. Distinguish confirmed defects from hypotheses and optional improvements; describe material costs or risks such as extra API calls.

Separate fixes that preserve existing requirements from changes to product policy or critical invariants. For a policy change, identify the current rule, proposed replacement, and the decision the user must make. Do not silently include it in the fix list or treat a generic instruction to implement recommended fixes as authorization to override that rule.

If no changes are justified, say why and do not invent work. If evidence is insufficient, identify the smallest missing evidence or targeted diagnostic change needed to decide; do not ask for another identical run when existing logging cannot resolve the uncertainty.

Keep the assessment concise and finish actionable recommendations with a scoped follow-up command, for example: “Впровадь рекомендовані виправлення 1 і 2”. The analysis itself does not authorize implementation; preserve the read-only scope until the user separately requests changes.
