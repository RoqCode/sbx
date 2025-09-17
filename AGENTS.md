# Repository Guidelines

## Project Structure & Module Organization
The CLI entrypoint lives in `cmd/sbx/main.go`, which bootstraps Cobra and routes to the packages under `internal`. Keep new Go code inside `internal/<domain>` so shared concerns stay scoped: `internal/cli` for command wiring, `internal/app` for app orchestration, `internal/fsutil` and `internal/infra` for platform helpers, and `internal/storyblok` for API-facing logic. JavaScript utilities that sync Storyblok assets live under `resources/`; keep companion data or mocks alongside the scripts they exercise.

## Build, Test & Development Commands
- `go run ./cmd/sbx --help` runs the CLI locally; use additional flags to exercise new commands.
- `go build ./cmd/sbx` produces the binary that ships with releases.
- `go test ./...` executes the full Go test suite; add `-run <Name>` when iterating on specific cases.
- `npm install` and `node resources/pull-components.js` (if your change affects Storyblok sync) hydrate local fixtures—document any additional prerequisites in your PR.

## Coding Style & Naming Conventions
Format Go code with `gofmt` (or `go fmt ./...`); run before sending reviews. Use descriptive package-level names and keep exported identifiers in PascalCase. Cobra commands and flags follow kebab-case (`--component-name`). Mirror the existing 2-space indentation and double-quoted strings in the JavaScript helpers. Prefer small files that isolate a single responsibility and colocate command wiring with its handler.

## Testing Guidelines
Place `*_test.go` files beside the code they validate and favor table-driven tests for matcher and Storyblok integrations. Exercise filesystem helpers with temporary directories rather than fixtures. Target at least smoke coverage for all new commands by stubbing external APIs. Run `go test -cover ./...` before pushing and call out any intentional coverage gaps.

## Commit & Pull Request Guidelines
Follow the Conventional Commits style already in history (`feat:`, `chore:`, `fix:`) and keep subject lines under 72 characters. Squash noisy WIP commits before review. Every PR should include a concise summary, reproduction or validation steps (`go test ./...`, `go run ./cmd/sbx ...`), and link to the relevant issue. Attach screenshots or terminal transcripts when behavior changes, and note any required Storyblok tokens without committing secrets.

## Configuration & Security Tips
Storyblok sync scripts expect `OAUTH_TOKEN` and space IDs; load them via environment variables or a local `.env` that stays untracked. Never commit API credentials or customer content. When debugging, prefer redacted logs written to the `push-debug.log` path already ignored by git.
