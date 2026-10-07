---
name: movielist-build
description: Build MovieList, perform a clean build, update its dependencies, or diagnose Go/Wails/Node build compatibility.
---

# MovieList Build

Work from the repository root. Follow [AGENTS.md](../../../AGENTS.md). Read current versions from `go.mod`, `frontend/package.json`, and installed tools rather than assuming remembered versions.

## Commands

- Production: `.\build.ps1`
- Development: `.\build.ps1 -Dev`
- Clean production: `.\build.ps1 -WailsArguments @('-clean', '-f')`
- Full rebuild when requested: `go clean -cache`, then `npm --prefix frontend ci`, then clean production. Check each exit status before continuing.

`-clean` clears `build/bin`; `-f` forces the Wails build. Cache clearing is optional and affects other local Go projects too. Use it when requested or when evidence points to stale cache. Avoid deleting module caches, user data, databases, posters, logs, or configuration.

The wrapper synchronizes release metadata before Wails reads it. Edit release versions only in `internal/version/VERSION`; never manually update generated version copies. Dependency updates do not themselves require a release bump. The executable is `build/bin/movielist-app.exe`. Do not launch it or start scans just to verify a build.

## Dependencies and compatibility

Inspect existing changes first. For requested dependency updates, check registry versions and runtime requirements, preserve Wails v2 and the Gemini SDK contract, and update manifests and lockfiles together. Run `go mod tidy` after Go dependency changes. Update documented tool requirements when needed.

Match Wails CLI to the module version in `go.mod`. Reinstall that explicit CLI version using the active Go compiler after a Go upgrade. Do not downgrade Go or migrate to Wails v3 to work around a build failure.

For `package ... without types`, inspect `go version` and `go version -m` on the executable resolved by `Get-Command wails`. Check the CLI compiler and `golang.org/x/tools` before changing application code. The error can indicate tool incompatibility but does not establish it by itself.

Dependency updates require Go/frontend tests, `go vet ./...`, `go mod verify`, and production build. For proportional checks on other changes, consult [MovieList Verification](../movielist-verify/SKILL.md). Respect current failure-handling instructions and report the failing stage.

Report actual versions, checks, and executable path. Successful builds do not prove real-library recognition or desktop UI behavior.
