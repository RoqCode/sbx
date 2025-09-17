# sbx CLI

`sbx` syncs Storyblok component schemas and presets between spaces so teams can version control definitions and promote them safely.

## Quick Start
1. Install Go 1.25+ and run `go build ./cmd/sbx` (or `go run ./cmd/sbx --help`).
2. Export credentials: `SB_MGMT_TOKEN`, `SOURCE_SPACE_ID`, `TARGET_SPACE_ID`.
3. Optional: set `SBX_OUT_DIR` to change the local schema directory (default `component-schemas/`).

## Global Flags
- `--token string` Storyblok management token (defaults to `SB_MGMT_TOKEN`).
- `--source-space int` Default source space ID (`SOURCE_SPACE_ID`).
- `--target-space int` Default target space ID (`TARGET_SPACE_ID`).
- `--out string` Local schema directory (`SBX_OUT_DIR`, falls back to `component-schemas/`).
- `-h, --help` Print command help.

## Commands & Usage
### Pull component schemas
Key flags: `--space` (override source space), `--match` (`exact|prefix|glob`), `--all`, `--dry-run`.
```
# Pull named components from the source space
sbx pull-components hero teaser

# Match by prefix and write to a custom folder
sbx pull-components --match prefix --out tmp/layout layout

# Preview planned actions without touching disk
sbx pull-components hero --dry-run
```

### Push component schemas
Key flags: `--space` (override target space), `--dir` (schema directory), `--match` (`exact|prefix|glob`), `--all`, `--dry-run`.
```
# Push everything under component-schemas/ to the target space
sbx push-components --all

# Push a subset from a different directory using glob matching
sbx push-components --dir dist --match glob "layout-*"

# Validate a push without mutating Storyblok
sbx push-components hero --dry-run
```

### Generate shell completion
Accepts `bash`, `zsh`, `fish`, or `powershell` as the shell argument.
```
sbx completion zsh > "${fpath[1]}/_sbx"
```
