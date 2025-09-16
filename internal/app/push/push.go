package push

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"sbx/internal/fsutil"
	"sbx/internal/infra/limiter"
	"sbx/internal/matcher"
	"sbx/internal/storyblok"
)

// Options defines configuration for pushing components to a target space.
type Options struct {
	Token     string
	SpaceID   int
	Names     []string
	MatchMode string
	All       bool
	Dir       string
	DryRun    bool
}

// Result summarises the outcome of the push operation.
type Result struct {
	ExitCode          int
	ComponentsSynced  int
	PresetsSynced     int
	Duration          time.Duration
	RateLimitRetries  int64
	MissingSelectors  []string
	CreatedComponents []string
	UpdatedComponents []string
}

var (
	errNoComponents = errors.New("no matching component files found")
)

var (
	colorInfo    = "\033[36m"
	colorWarn    = "\033[33m"
	colorSuccess = "\033[32m"
	colorReset   = "\033[0m"
	useColor     = enableColor()
)

// Run executes the push workflow.
func Run(ctx context.Context, opts Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	result := Result{ExitCode: 0}

	if err := matcher.ValidateMode(opts.MatchMode); err != nil {
		return result, err
	}

	if !opts.All && len(opts.Names) == 0 {
		return result, fmt.Errorf("no component names provided; use --all to push every component")
	}

	start := time.Now()

	lim := limiter.NewSpaceLimiter(7, 7, 7)
	client := storyblok.NewClient(opts.Token, storyblok.WithLimiter(lim))

	counters := &storyblok.RetryCounters{}
	ctx = storyblok.WithRetryCounters(ctx, counters)

	componentFiles, err := discoverComponentFiles(opts)
	if err != nil {
		return result, err
	}
	if len(componentFiles) == 0 {
		return result, errNoComponents
	}

	components, err := loadComponents(componentFiles)
	if err != nil {
		return result, err
	}
	if len(components) == 0 {
		return result, errNoComponents
	}
	infof("Loaded %d component files from %s", len(components), opts.Dir)

	selectedComponents, missing, err := matcher.Filter(components, func(cf ComponentFile) string {
		return cf.Component.Name
	}, opts.Names, opts.MatchMode, opts.All)
	if err != nil {
		return result, err
	}

	result.MissingSelectors = missing
	if len(missing) > 0 {
		result.ExitCode = 1
	}
	infof("Selected %d components (missing: %d)", len(selectedComponents), len(missing))

	presetFiles, err := discoverPresetFiles(opts)
	if err != nil {
		return result, err
	}
	infof("Discovered %d preset files", len(presetFiles))

	presetMap := buildPresetMap(presetFiles)

	eg, egCtx := errgroup.WithContext(ctx)

	var targetComponents []storyblok.Component
	eg.Go(func() error {
		list, err := client.ListComponents(egCtx, opts.SpaceID)
		if err != nil {
			return err
		}
		targetComponents = list
		return nil
	})

	var targetGroups []storyblok.ComponentGroup
	eg.Go(func() error {
		list, err := client.ListComponentGroups(egCtx, opts.SpaceID)
		if err != nil {
			return err
		}
		targetGroups = list
		return nil
	})

	var targetPresets []storyblok.ComponentPreset
	eg.Go(func() error {
		list, err := client.ListPresets(egCtx, opts.SpaceID)
		if err != nil {
			return err
		}
		targetPresets = list
		return nil
	})

	var targetTags []storyblok.InternalTag
	eg.Go(func() error {
		list, err := client.ListInternalTags(egCtx, opts.SpaceID)
		if err != nil {
			return err
		}
		targetTags = list
		return nil
	})

	if err := eg.Wait(); err != nil {
		warnf("failed to load target space metadata: %v", err)
		result.ExitCode = 2
		return result, err
	}
	infof("Target space has %d components, %d groups, %d presets, %d tags", len(targetComponents), len(targetGroups), len(targetPresets), len(targetTags))

	groupUUIDByName := make(map[string]string, len(targetGroups))
	for _, g := range targetGroups {
		if g.Name != "" {
			groupUUIDByName[strings.ToLower(g.Name)] = g.UUID
		}
	}

	targetByName := make(map[string]storyblok.Component, len(targetComponents))
	for _, comp := range targetComponents {
		targetByName[strings.ToLower(comp.Name)] = comp
	}

	tagIDByName := make(map[string]int, len(targetTags))
	for _, tag := range targetTags {
		tagIDByName[tag.Name] = tag.ID
	}

	var created, updated []string

	for i, plan := range selectedComponents {
		component := plan.Component
		infof("[%d/%d] syncing component %s", i+1, len(selectedComponents), component.Name)

		if plan.Component.ComponentGroupName != "" {
			uuid, err := ensureComponentGroup(ctx, client, opts.SpaceID, groupUUIDByName, plan.Component.ComponentGroupName)
			if err != nil {
				result.ExitCode = 2
				return result, err
			}
			component.ComponentGroupUUID = uuid
			component.ComponentGroupName = ""
		}

		if err := mapSchemaGroupWhitelist(&component, groupUUIDByName); err != nil {
			result.ExitCode = 2
			return result, err
		}

		tagIDs, err := ensureInternalTags(ctx, client, opts.SpaceID, tagIDByName, component.InternalTagsList)
		if err != nil {
			result.ExitCode = 2
			return result, err
		}
		component.InternalTagIDs = storyblok.IntSlice(tagIDs)

		existing, exists := targetByName[strings.ToLower(component.Name)]
		componentPresets := presetsForComponent(component, presetMap)
		infof("Component %s has %d preset candidates", component.Name, len(componentPresets))

		if opts.DryRun {
			logDryRun(component, exists, opts.SpaceID, len(componentPresets))
			result.ComponentsSynced++
			result.PresetsSynced += len(componentPresets)
			continue
		}

		if exists {
			successf("Updating component %s (id=%d)", component.Name, existing.ID)
			if err := updateComponent(ctx, client, opts.SpaceID, existing, component, componentPresets, targetPresets); err != nil {
				result.ExitCode = 2
				return result, err
			}
			updated = append(updated, component.Name)
		} else {
			successf("Creating component %s", component.Name)
			if err := createComponent(ctx, client, opts.SpaceID, component, componentPresets, &created, targetByName); err != nil {
				result.ExitCode = 2
				return result, err
			}
		}

		result.ComponentsSynced++
		result.PresetsSynced += len(componentPresets)
	}

	sort.Strings(created)
	sort.Strings(updated)

	result.CreatedComponents = created
	result.UpdatedComponents = updated
	result.RateLimitRetries = counters.Status429.Load()
	result.Duration = time.Since(start)

	printPushSummary(result, opts)

	return result, nil
}

// ComponentFile couples a component payload with its source file path.
type ComponentFile struct {
	Path      string
	Component storyblok.Component
}

// PresetFile couples a preset payload with its source file path.
type PresetFile struct {
	Path   string
	Preset storyblok.ComponentPreset
}

func discoverComponentFiles(opts Options) ([]string, error) {
	dirs := candidateDirs(opts.Dir, "components")
	set := make(map[string]struct{})
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			path := filepath.Join(dir, name)
			set[path] = struct{}{}
		}
	}
	files := make([]string, 0, len(set))
	for path := range set {
		files = append(files, path)
	}
	sort.Strings(files)
	return files, nil
}

func discoverPresetFiles(opts Options) ([]PresetFile, error) {
	dirs := candidateDirs(opts.Dir, "presets")
	var presets []PresetFile
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			path := filepath.Join(dir, name)
			var preset storyblok.ComponentPreset
			if err := fsutil.ReadJSON(path, &preset); err != nil {
				warnf("Skipping invalid preset file %s: %v", path, err)
				continue
			}
			if preset.Name == "" || preset.Preset == nil {
				warnf("Skipping preset file %s without name or preset content", path)
				continue
			}
			presets = append(presets, PresetFile{Path: path, Preset: preset})
		}
	}
	sort.Slice(presets, func(i, j int) bool { return presets[i].Path < presets[j].Path })
	return presets, nil
}

func loadComponents(files []string) ([]ComponentFile, error) {
	components := make([]ComponentFile, 0, len(files))
	for _, path := range files {
		var comp storyblok.Component
		if err := fsutil.ReadJSON(path, &comp); err != nil {
			warnf("Skipping invalid component file %s: %v", path, err)
			continue
		}
		if comp.Name == "" || comp.Schema == nil {
			warnf("Skipping component file %s without name or schema", path)
			continue
		}
		components = append(components, ComponentFile{Path: path, Component: comp})
	}
	return components, nil
}

func candidateDirs(base, sub string) []string {
	dirs := make([]string, 0, 2)
	subPath := filepath.Join(base, sub)
	if info, err := os.Stat(subPath); err == nil && info.IsDir() {
		dirs = append(dirs, subPath)
	}
	dirs = append(dirs, base)
	return dirs
}

func enableColor() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	return true
}

func infof(format string, args ...any) {
	logWithColor(colorInfo, format, args...)
}

func warnf(format string, args ...any) {
	logWithColor(colorWarn, format, args...)
}

func successf(format string, args ...any) {
	logWithColor(colorSuccess, format, args...)
}

func logWithColor(color, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	if useColor {
		fmt.Fprintf(os.Stderr, "%s%s%s\n", color, message, colorReset)
		return
	}
	fmt.Fprintf(os.Stderr, "%s\n", message)
}

func buildPresetMap(presets []PresetFile) map[string][]storyblok.ComponentPreset {
	presetsByComponent := make(map[string][]storyblok.ComponentPreset)
	for _, preset := range presets {
		compName, _ := preset.Preset.Preset["component"].(string)
		if compName == "" {
			continue
		}
		key := strings.ToLower(compName)
		presetsByComponent[key] = append(presetsByComponent[key], preset.Preset)
	}
	return presetsByComponent
}

func presetsForComponent(component storyblok.Component, presetMap map[string][]storyblok.ComponentPreset) []storyblok.ComponentPreset {
	name := strings.ToLower(component.Name)
	return presetMap[name]
}

func ensureComponentGroup(ctx context.Context, client *storyblok.Client, spaceID int, groups map[string]string, groupName string) (string, error) {
	key := strings.ToLower(groupName)
	if uuid, ok := groups[key]; ok {
		return uuid, nil
	}
	created, err := client.CreateComponentGroup(ctx, spaceID, storyblok.ComponentGroup{Name: groupName})
	if err != nil {
		return "", err
	}
	groups[key] = created.UUID
	return created.UUID, nil
}

func mapSchemaGroupWhitelist(component *storyblok.Component, groups map[string]string) error {
	if component.Schema == nil {
		return nil
	}
	for _, value := range component.Schema {
		fieldMap, ok := value.(map[string]any)
		if !ok {
			continue
		}
		whitelist, ok := fieldMap["component_group_whitelist"].([]any)
		if !ok {
			continue
		}
		mapped := make([]any, 0, len(whitelist))
		for _, item := range whitelist {
			value, _ := item.(string)
			if value == "" {
				continue
			}
			key := strings.ToLower(value)
			if uuid, ok := groups[key]; ok {
				mapped = append(mapped, uuid)
			} else {
				mapped = append(mapped, value)
			}
		}
		fieldMap["component_group_whitelist"] = mapped
	}
	return nil
}

func ensureInternalTags(ctx context.Context, client *storyblok.Client, spaceID int, tags map[string]int, source []storyblok.InternalTag) ([]int, error) {
	if len(source) == 0 {
		return nil, nil
	}
	var ids []int
	for _, tag := range source {
		if tag.Name == "" {
			continue
		}
		if id, ok := tags[tag.Name]; ok {
			ids = append(ids, id)
			continue
		}
		created, err := client.CreateInternalTag(ctx, spaceID, storyblok.InternalTag{Name: tag.Name, ObjectType: "component"})
		if err != nil {
			return nil, err
		}
		tags[tag.Name] = created.ID
		ids = append(ids, created.ID)
	}
	return ids, nil
}

func logDryRun(component storyblok.Component, exists bool, spaceID int, presetCount int) {
	action := "create"
	if exists {
		action = "update"
	}
	fmt.Printf("Dry run: %s component %s in space %d (%d presets)\n", action, component.Name, spaceID, presetCount)
}

func createComponent(ctx context.Context, client *storyblok.Client, spaceID int, component storyblok.Component, presets []storyblok.ComponentPreset, created *[]string, targetByName map[string]storyblok.Component) error {
	defaultName := defaultPresetName(component, presets)
	component.PresetID = 0
	createdComponent, err := client.CreateComponent(ctx, spaceID, component)
	if err != nil {
		return err
	}
	*created = append(*created, createdComponent.Name)
	targetByName[strings.ToLower(createdComponent.Name)] = createdComponent

	if len(presets) == 0 {
		return nil
	}

	createdPresets := make([]storyblok.ComponentPreset, 0, len(presets))
	for _, preset := range presets {
		preset.ComponentID = createdComponent.ID
		preset.ID = 0
		newPreset, err := client.CreatePreset(ctx, spaceID, preset)
		if err != nil {
			return err
		}
		createdPresets = append(createdPresets, newPreset)
	}

	if defaultName != "" {
		if targetPreset, ok := findPresetByName(createdPresets, defaultName); ok {
			createdComponent.PresetID = targetPreset.ID
			if _, err := client.UpdateComponent(ctx, spaceID, createdComponent.ID, createdComponent); err != nil {
				return err
			}
		}
	}

	return nil
}

func defaultPresetName(component storyblok.Component, presets []storyblok.ComponentPreset) string {
	if component.PresetID == 0 {
		return ""
	}
	id := component.PresetID
	for _, preset := range presets {
		if preset.ID == id {
			return strings.ToLower(preset.Name)
		}
	}
	return ""
}

func findPresetByName(presets []storyblok.ComponentPreset, name string) (storyblok.ComponentPreset, bool) {
	key := strings.ToLower(name)
	for _, preset := range presets {
		if strings.ToLower(preset.Name) == key {
			return preset, true
		}
	}
	return storyblok.ComponentPreset{}, false
}

func updateComponent(ctx context.Context, client *storyblok.Client, spaceID int, existing storyblok.Component, updated storyblok.Component, presets []storyblok.ComponentPreset, targetPresets []storyblok.ComponentPreset) error {
	defaultName := defaultPresetName(updated, presets)
	updated.ID = existing.ID
	updated.PresetID = 0

	if _, err := client.UpdateComponent(ctx, spaceID, existing.ID, updated); err != nil {
		return err
	}

	existingPresets := map[string]storyblok.ComponentPreset{}
	for _, preset := range targetPresets {
		if preset.ComponentID == existing.ID {
			existingPresets[strings.ToLower(preset.Name)] = preset
		}
	}

	for _, preset := range presets {
		key := strings.ToLower(preset.Name)
		preset.ComponentID = existing.ID
		if existingPreset, ok := existingPresets[key]; ok {
			preset.ID = existingPreset.ID
			updatedPreset, err := client.UpdatePreset(ctx, spaceID, preset)
			if err != nil {
				return err
			}
			existingPresets[key] = updatedPreset
		} else {
			preset.ID = 0
			createdPreset, err := client.CreatePreset(ctx, spaceID, preset)
			if err != nil {
				return err
			}
			existingPresets[key] = createdPreset
		}
	}

	if defaultName != "" {
		if targetPreset, ok := existingPresets[defaultName]; ok {
			if targetPreset.ID != existing.PresetID {
				updated.PresetID = targetPreset.ID
				if _, err := client.UpdateComponent(ctx, spaceID, existing.ID, updated); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func printPushSummary(result Result, opts Options) {
	if opts.DryRun {
		fmt.Println()
		fmt.Printf("Dry run summary: %d components, %d presets (rate-limit retries: %d)\n",
			result.ComponentsSynced,
			result.PresetsSynced,
			result.RateLimitRetries,
		)
		if len(result.MissingSelectors) > 0 {
			fmt.Fprintf(os.Stderr, "Missing components matching: %s\n", strings.Join(result.MissingSelectors, ", "))
		}
		return
	}

	fmt.Println()
	fmt.Printf("Pushed %d components and %d presets to space %d in %s (rate-limit retries: %d)\n",
		result.ComponentsSynced,
		result.PresetsSynced,
		opts.SpaceID,
		result.Duration.Truncate(time.Millisecond),
		result.RateLimitRetries,
	)
	if len(result.CreatedComponents) > 0 {
		fmt.Printf("  Created: %s\n", strings.Join(result.CreatedComponents, ", "))
	}
	if len(result.UpdatedComponents) > 0 {
		fmt.Printf("  Updated: %s\n", strings.Join(result.UpdatedComponents, ", "))
	}
	if len(result.MissingSelectors) > 0 {
		fmt.Fprintf(os.Stderr, "Missing components matching: %s\n", strings.Join(result.MissingSelectors, ", "))
	}
}
