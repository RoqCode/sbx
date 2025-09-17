# sbx Planning

## Goals & Scope
- Ship a non-interactive CLI (`sbx`) for syncing Storyblok component schemas and presets between spaces, optimised for CI pipelines.
- Maintain functional parity with Storyblok CLI `pull-components`/`push-components` (separate schema/preset files, component groups, internal tags, preset handling, dry-run, exit codes).
- Reuse `storyblok-sync` concurrency + rate limiting patterns to keep throughput high while respecting Storyblok limits.
- Build a maintainable Go codebase with room for future enhancements (verbose logging, additional commands, other entity sync).

## Confirmed Decisions
- Auth: management token via `SB_MGMT_TOKEN` only (MVP scope).
- Space selection: env vars `SOURCE_SPACE_ID` / `TARGET_SPACE_ID` plus CLI overrides.
- File layout: no aggregate JSON; each component and preset saved as `<name>-<spaceId>.json` in separate files, matching Storyblok CLI behaviour.
- CLI UX: Cobra-based commands with shell completion; no interactive prompts.
- Logging: match Storyblok CLI surface-level messaging now; plan `--verbose` for later.
- Default output dir: `component-schemas/` if `--out` not provided; flag allows custom directories.
- Worker pool: four concurrent workers for push/pull operations; revisit if needed.
- Datasource resolution: omitted in MVP (future flag optional).
- End-of-run report: printed to stdout summarising component count, elapsed duration, and rate-limit retry total.
- Dry-run mode: list components/presets to sync, indicate source/target spaces, and highlight overwrite scenarios (target component update or local file overwrite).

## Implementation Plan
1. **Project Scaffolding** – ✅ complete
   - Cobra CLI skeleton, shared flags/options, dry-run/reporting hooks, and shell completions are in place.

2. **Storyblok API Client** – ✅ complete
   - Typed client, limiter/backoff port, and error helpers implemented.

3. **Domain Models & Storage Layout** – ✅ complete
   - Component/preset structs with raw preservation, filesystem helpers, and matchers shipped.

4. **Pull Command Implementation** – ✅ complete
   - Pull workflow fetches data concurrently, writes individual files, supports dry-run/exits, and reports retries.

5. **Push Command Implementation** – ⚠️ in progress
   - Discover component/preset files based on args/match/all; validate JSON and load into domain structs.
   - Ensure component groups exist (recursive create) referencing Storyblok CLI logic; remap whitelist UUIDs via group names.
   - Ensure internal tags exist, diff presets (create/update/default) as per `components.js` + `presets-lib.js`.
   - Execute operations via four-worker pool using limiter and retry/backoff; track when updates overwrite existing components for reports and dry-run output. *(worker pool + preset diff polish still pending)*
   - Aggregate results for summary and exit codes (0 success, 1 validation/not found, 2 API error).

6. **Shared Utilities** – ✅ complete
   - Retry counters, colored logging, and reporting helpers available.

7. **Testing Strategy** – ⬜ not started
   - Unit tests for matcher utilities, filesystem helpers (including overwrite detection), limiter behaviour, preset diffing, group/tag mapping, and report aggregation.
   - HTTP mock tests covering pull/push flows with rate-limit responses to ensure retry counting and limiter adjustments work.
   - Optional manual integration script (documented) for real-space smoke testing post-MVP.

8. **Developer Experience & Distribution** – ⬜ not started
   - Add `Makefile` or Go task aliases for lint/test/build.
   - Document usage, configuration, dry-run/report output, and CI integration in README + planning notes.
   - Note future release automation tasks (e.g., goreleaser) as post-MVP considerations.

## Open Points Before Coding
- None outstanding—requirements clarified for MVP. Ready to start implementation.
