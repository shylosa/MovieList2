---
name: movielist-verify
description: Choose and run proportional checks after MovieList code or build changes, or when verification is requested.
---

# MovieList Verification

Follow [AGENTS.md](../../../AGENTS.md). Determine changed files and behavior from `git status` and relevant diffs; preserve user changes. Run checks from the repository root. Expand for cross-package effects or unresolved failures.

| Change | Verification |
| --- | --- |
| Documentation or skill instructions only | Links, UTF-8 without BOM, LF, and `git diff --check`; validate changed skills with the available skill-creator validator. No application build needed. |
| Go implementation | `gofmt` changed Go files; affected package tests and appropriate `go vet` checks. Include the root package when orchestration or exported behavior is affected. |
| Shared recognition, storage, config, lifecycle, or Wails API behavior | `go test ./...` and `go vet ./...`; frontend tests when the contract affects UI. |
| Frontend logic | `npm --prefix frontend test` and `npm --prefix frontend run build`. |
| CSS or static visual assets only | Frontend build; identify remaining visual runtime checks. |
| Release source or version synchronization | `npm --prefix frontend test`, `go test ./internal/version`, and `.\build.ps1`. Never manually edit generated version copies. |
| Dependencies, Go version, or build tooling | `go test ./...`, `go vet ./...`, `go mod verify`, frontend tests, and `.\build.ps1`. Include npm audit when npm dependencies change. |

The production wrapper builds the frontend; avoid a redundant standalone frontend build when the same final state is about to be checked by the wrapper.

Default Go tests use isolated API transports. Do not enable `MOVIELIST_LIVE_*` flags, inference, exports, deployment, or real scans as routine checks. Live checks require explicit authorization for external requests and quota usage.

Add regression tests for meaningful failure modes, not merely to mirror implementation or document wording. Do not repeat passing checks unless subsequent changes invalidate them. Respect current failure-handling instructions; record failed or unavailable checks explicitly.

Report outcomes and material gaps. Automated tests and synthetic provider checks do not complete manual Wails UI or full production scan checks. Mark `CHECKLIST.md` items complete only when implementation and proportional verification support that exact item.
