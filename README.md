# sbx CLI

`sbx` syncs Storyblok component schemas and presets between spaces so teams can version control definitions and promote them safely.

## Quick Start
1. Install Go 1.25+ and run `go build ./cmd/sbx` (or `go run ./cmd/sbx --help`).
2. Export credentials: `SB_MGMT_TOKEN`, `SOURCE_SPACE_ID`, `TARGET_SPACE_ID`.
3. Optional: set `SBX_OUT_DIR` to change the local schema directory (default `component-schemas/`).

## CLI Overview
```
sbx [command] [flags]
  --token        Storyblok management token (SB_MGMT_TOKEN)
  --source-space Source space ID (SOURCE_SPACE_ID)
  --target-space Target space ID (TARGET_SPACE_ID)
  --out          Schema output directory (SBX_OUT_DIR or component-schemas/)
```

## Usage Examples
### Pull component schemas
```
# Pull named components from the source space
sbx pull-components hero teaser

# Match by prefix and write to a custom folder
sbx pull-components --match prefix --out tmp/layout layout

# Preview planned actions without touching disk
sbx pull-components hero --dry-run
```

### Push component schemas
```
# Push everything under component-schemas/ to the target space
sbx push-components --all

# Push a subset from a different directory using glob matching
sbx push-components --dir dist --match glob "layout-*"

# Validate a push without mutating Storyblok
sbx push-components hero --dry-run
```

### Generate shell completion
```
sbx completion zsh > "${fpath[1]}/_sbx"
```
