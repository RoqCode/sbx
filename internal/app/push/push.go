package push

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

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
	ExitCode           int
	ComponentsSynced   int
	PresetsSynced      int
	Duration           time.Duration
	RateLimitRetries   int64
	ServerErrorRetries int64
	MissingSelectors   []string
	CreatedComponents  []string
	UpdatedComponents  []string
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

type groupCache struct {
	mu     sync.RWMutex
	data   map[string]string
	single singleflight.Group
}

func newGroupCache() *groupCache {
	return &groupCache{data: make(map[string]string)}
}

func (c *groupCache) Set(name, uuid string) {
	if name == "" || uuid == "" {
		return
	}
	key := strings.ToLower(strings.TrimSpace(name))
	c.mu.Lock()
	c.data[key] = uuid
	c.mu.Unlock()
}

func (c *groupCache) Lookup(name string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	c.mu.RLock()
	value, ok := c.data[key]
	c.mu.RUnlock()
	return value, ok
}

func (c *groupCache) Has(name string) bool {
	_, ok := c.Lookup(name)
	return ok
}

type tagCache struct {
	mu     sync.RWMutex
	data   map[string]int
	single singleflight.Group
}

func newTagCache() *tagCache {
	return &tagCache{data: make(map[string]int)}
}

func (c *tagCache) Set(name string, id int) {
	if name == "" || id <= 0 {
		return
	}
	key := strings.TrimSpace(name)
	if key == "" {
		return
	}
	c.mu.Lock()
	c.data[key] = id
	c.mu.Unlock()
}

func (c *tagCache) Get(name string) (int, bool) {
	key := strings.TrimSpace(name)
	if key == "" {
		return 0, false
	}
	c.mu.RLock()
	id, ok := c.data[key]
	c.mu.RUnlock()
	return id, ok
}

func (c *tagCache) Has(name string) bool {
	_, ok := c.Get(name)
	return ok
}

type componentCache struct {
	mu   sync.RWMutex
	data map[string]storyblok.Component
}

func newComponentCache() *componentCache {
	return &componentCache{data: make(map[string]storyblok.Component)}
}

func (c *componentCache) Set(name string, component storyblok.Component) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return
	}
	c.mu.Lock()
	c.data[key] = component
	c.mu.Unlock()
}

type componentPlan struct {
	index     int
	component storyblok.Component
	existing  storyblok.Component
	exists    bool
	presets   []storyblok.ComponentPreset
}

type componentOutcome struct {
	index       int
	name        string
	componentID int
	presets     int
	created     bool
	updated     bool
}

func logSyncOutcome(outcome componentOutcome) {
	if outcome.name == "" {
		return
	}
	if outcome.created {
		successf("Created component %s (id=%d)", outcome.name, outcome.componentID)
		return
	}
	if outcome.updated {
		successf("Updated component %s (id=%d)", outcome.name, outcome.componentID)
	}
}

type componentProcessor struct {
	client        *storyblok.Client
	spaceID       int
	groups        *groupCache
	tags          *tagCache
	components    *componentCache
	targetPresets []storyblok.ComponentPreset
}

func (p *componentProcessor) Process(ctx context.Context, plan componentPlan) (componentOutcome, error) {
	component := plan.component
	infof("Syncing component %s", component.Name)
	infof("Component %s has %d preset candidates", component.Name, len(plan.presets))
	if plan.component.ComponentGroupName != "" {
		uuid, err := ensureComponentGroup(ctx, p.client, p.spaceID, p.groups, plan.component.ComponentGroupName)
		if err != nil {
			return componentOutcome{}, err
		}
		component.ComponentGroupUUID = uuid
		component.ComponentGroupName = ""
	}

	if err := mapSchemaGroupWhitelist(&component, p.groups.Lookup); err != nil {
		return componentOutcome{}, err
	}

	tagIDs, err := ensureInternalTags(ctx, p.client, p.spaceID, p.tags, component.InternalTagsList)
	if err != nil {
		return componentOutcome{}, err
	}
	component.InternalTagIDs = storyblok.IntSlice(tagIDs)

	outcome := componentOutcome{
		index:   plan.index,
		name:    component.Name,
		presets: len(plan.presets),
	}

	if plan.exists {
		updatedComp, err := updateComponent(ctx, p.client, p.spaceID, plan.existing, component, plan.presets, p.targetPresets)
		if err != nil {
			return outcome, err
		}
		outcome.updated = true
		outcome.name = updatedComp.Name
		outcome.componentID = updatedComp.ID
		if !strings.EqualFold(plan.existing.Name, updatedComp.Name) {
			p.components.Replace(plan.existing.Name, updatedComp.Name, updatedComp)
		} else {
			p.components.Set(updatedComp.Name, updatedComp)
		}
	} else {
		createdComp, err := createComponent(ctx, p.client, p.spaceID, component, plan.presets)
		if err != nil {
			return outcome, err
		}
		outcome.created = true
		outcome.name = createdComp.Name
		outcome.componentID = createdComp.ID
		p.components.Set(createdComp.Name, createdComp)
	}

	return outcome, nil
}

func (c *componentCache) Get(name string) (storyblok.Component, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	c.mu.RLock()
	component, ok := c.data[key]
	c.mu.RUnlock()
	return component, ok
}

func (c *componentCache) Replace(oldName, newName string, component storyblok.Component) {
	oldKey := strings.ToLower(strings.TrimSpace(oldName))
	newKey := strings.ToLower(strings.TrimSpace(newName))
	c.mu.Lock()
	if oldKey != "" {
		delete(c.data, oldKey)
	}
	if newKey != "" {
		c.data[newKey] = component
	}
	c.mu.Unlock()
}

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

	groupCache := newGroupCache()
	for _, g := range targetGroups {
		if g.Name != "" && g.UUID != "" {
			groupCache.Set(g.Name, g.UUID)
		}
	}

	componentCache := newComponentCache()
	for _, comp := range targetComponents {
		componentCache.Set(comp.Name, comp)
	}

	tagCache := newTagCache()
	for _, tag := range targetTags {
		if tag.Name != "" && tag.ID > 0 {
			tagCache.Set(tag.Name, tag.ID)
		}
	}

	var plans []componentPlan
	plans = make([]componentPlan, 0, len(selectedComponents))

	for _, plan := range selectedComponents {
		component := plan.Component

		existing, exists := componentCache.Get(component.Name)
		componentPresets := presetsForComponent(component, presetMap)

		if opts.DryRun {
			logDryRun(component, exists, opts.SpaceID, len(componentPresets), groupCache.Has, tagCache.Has)
			result.ComponentsSynced++
			result.PresetsSynced += len(componentPresets)
			continue
		}

		idx := len(plans)
		plans = append(plans, componentPlan{
			index:     idx,
			component: component,
			existing:  existing,
			exists:    exists,
			presets:   componentPresets,
		})
	}

	var created, updated []string

	if !opts.DryRun && len(plans) > 0 {
		processor := componentProcessor{
			client:        client,
			spaceID:       opts.SpaceID,
			groups:        groupCache,
			tags:          tagCache,
			components:    componentCache,
			targetPresets: targetPresets,
		}

		workerCount := 4
		if len(plans) < workerCount {
			workerCount = len(plans)
		}
		if workerCount < 1 {
			workerCount = 1
		}

		jobs := make(chan componentPlan)
		outcomes := make([]componentOutcome, len(plans))
		var outcomeMu sync.Mutex
		egWorkers, egCtx := errgroup.WithContext(ctx)

		for i := 0; i < workerCount; i++ {
			egWorkers.Go(func() error {
				for {
					select {
					case <-egCtx.Done():
						return egCtx.Err()
					case job, ok := <-jobs:
						if !ok {
							return nil
						}
						outcome, err := processor.Process(egCtx, job)
						if err != nil {
							return err
						}
						outcomeMu.Lock()
						outcomes[job.index] = outcome
						outcomeMu.Unlock()
						logSyncOutcome(outcome)
					}
				}
			})
		}

		go func() {
			defer close(jobs)
			for _, job := range plans {
				select {
				case <-egCtx.Done():
					return
				case jobs <- job:
				}
			}
		}()

		if err := egWorkers.Wait(); err != nil {
			result.ExitCode = 2
			return result, err
		}

		for _, outcome := range outcomes {
			if outcome.name == "" {
				continue
			}
			if outcome.created {
				created = append(created, outcome.name)
			} else if outcome.updated {
				updated = append(updated, outcome.name)
			}
			result.ComponentsSynced++
			result.PresetsSynced += outcome.presets
		}
	}

	sort.Strings(created)
	sort.Strings(updated)

	result.CreatedComponents = created
	result.UpdatedComponents = updated
	result.RateLimitRetries = counters.Status429.Load()
	result.ServerErrorRetries = counters.Status5xx.Load()
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
	var parseErrors []string
	var missingFields []string
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
				parseErrors = append(parseErrors, fmt.Sprintf("%s (%v)", filepath.Base(path), err))
				continue
			}
			if preset.Name == "" || preset.Preset == nil {
				missingFields = append(missingFields, filepath.Base(path))
				continue
			}
			presets = append(presets, PresetFile{Path: path, Preset: preset})
		}
	}
	sort.Slice(presets, func(i, j int) bool { return presets[i].Path < presets[j].Path })
	if len(parseErrors) > 0 {
		warnf("Skipped %d preset files with parse errors: %s", len(parseErrors), summarizeList(parseErrors, 3))
	}
	if len(missingFields) > 0 {
		warnf("Skipped %d preset files missing name/preset: %s", len(missingFields), summarizeList(missingFields, 3))
	}
	return presets, nil
}

func loadComponents(files []string) ([]ComponentFile, error) {
	components := make([]ComponentFile, 0, len(files))
	var parseErrors []string
	var missingFields []string
	for _, path := range files {
		var comp storyblok.Component
		if err := fsutil.ReadJSON(path, &comp); err != nil {
			parseErrors = append(parseErrors, fmt.Sprintf("%s (%v)", filepath.Base(path), err))
			continue
		}
		if comp.Name == "" || comp.Schema == nil {
			missingFields = append(missingFields, filepath.Base(path))
			continue
		}
		components = append(components, ComponentFile{Path: path, Component: comp})
	}
	if len(parseErrors) > 0 {
		warnf("Skipped %d component files with parse errors: %s", len(parseErrors), summarizeList(parseErrors, 3))
	}
	if len(missingFields) > 0 {
		warnf("Skipped %d component files missing name/schema: %s", len(missingFields), summarizeList(missingFields, 3))
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

func ensureComponentGroup(ctx context.Context, client *storyblok.Client, spaceID int, groups *groupCache, groupName string) (string, error) {
	if groupName == "" {
		return "", nil
	}
	if uuid, ok := groups.Lookup(groupName); ok {
		return uuid, nil
	}

	key := strings.ToLower(strings.TrimSpace(groupName))
	if key == "" {
		return "", nil
	}

	value, err, _ := groups.single.Do(key, func() (any, error) {
		if uuid, ok := groups.Lookup(groupName); ok {
			return uuid, nil
		}
		created, err := client.CreateComponentGroup(ctx, spaceID, storyblok.ComponentGroup{Name: groupName})
		if err != nil {
			return "", err
		}
		groups.Set(groupName, created.UUID)
		return created.UUID, nil
	})
	if err != nil {
		return "", err
	}
	uuid, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("unexpected component group result type %T", value)
	}
	return uuid, nil
}

func mapSchemaGroupWhitelist(component *storyblok.Component, lookup func(string) (string, bool)) error {
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
			if uuid, ok := lookup(value); ok {
				mapped = append(mapped, uuid)
			} else {
				mapped = append(mapped, value)
			}
		}
		fieldMap["component_group_whitelist"] = mapped
	}
	return nil
}

func ensureInternalTags(ctx context.Context, client *storyblok.Client, spaceID int, tags *tagCache, source []storyblok.InternalTag) ([]int, error) {
	if len(source) == 0 {
		return nil, nil
	}
	var ids []int
	for _, tag := range source {
		name := strings.TrimSpace(tag.Name)
		if name == "" {
			continue
		}
		if id, ok := tags.Get(name); ok {
			ids = append(ids, id)
			continue
		}
		value, err, _ := tags.single.Do(name, func() (any, error) {
			if id, ok := tags.Get(name); ok {
				return id, nil
			}
			created, err := client.CreateInternalTag(ctx, spaceID, storyblok.InternalTag{Name: name, ObjectType: "component"})
			if err != nil {
				return 0, err
			}
			tags.Set(name, created.ID)
			return created.ID, nil
		})
		if err != nil {
			return nil, err
		}
		id, ok := value.(int)
		if !ok {
			return nil, fmt.Errorf("unexpected internal tag result type %T", value)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func logDryRun(component storyblok.Component, exists bool, spaceID int, presetCount int, hasGroup func(string) bool, hasTag func(string) bool) {
	action := "create"
	if exists {
		action = "update"
	}
	fmt.Printf("Dry run: %s component %s in space %d (%d presets)\n", action, component.Name, spaceID, presetCount)

	if component.ComponentGroupName != "" {
		if !hasGroup(component.ComponentGroupName) {
			fmt.Printf("  - would create component group %q\n", component.ComponentGroupName)
		}
	}

	if len(component.InternalTagsList) > 0 {
		missing := make([]string, 0, len(component.InternalTagsList))
		for _, tag := range component.InternalTagsList {
			name := strings.TrimSpace(tag.Name)
			if name == "" {
				continue
			}
			if !hasTag(name) {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			fmt.Printf("  - would create internal tags: %s\n", strings.Join(missing, ", "))
		}
	}
}

func createComponent(ctx context.Context, client *storyblok.Client, spaceID int, component storyblok.Component, presets []storyblok.ComponentPreset) (storyblok.Component, error) {
	defaultName := defaultPresetName(component, presets)
	component.PresetID = 0
	createdComponent, err := client.CreateComponent(ctx, spaceID, component)
	if err != nil {
		return storyblok.Component{}, err
	}

	if len(presets) == 0 {
		return createdComponent, nil
	}

	createdPresets := make([]storyblok.ComponentPreset, 0, len(presets))
	for _, preset := range presets {
		preset.ComponentID = createdComponent.ID
		preset.ID = 0
		newPreset, err := client.CreatePreset(ctx, spaceID, preset)
		if err != nil {
			return storyblok.Component{}, err
		}
		createdPresets = append(createdPresets, newPreset)
	}

	if defaultName != "" {
		if targetPreset, ok := findPresetByName(createdPresets, defaultName); ok {
			createdComponent.PresetID = targetPreset.ID
			if updatedComponent, err := client.UpdateComponent(ctx, spaceID, createdComponent.ID, createdComponent); err != nil {
				return storyblok.Component{}, err
			} else {
				createdComponent = updatedComponent
			}
		}
	}

	return createdComponent, nil
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

func updateComponent(ctx context.Context, client *storyblok.Client, spaceID int, existing storyblok.Component, updated storyblok.Component, presets []storyblok.ComponentPreset, targetPresets []storyblok.ComponentPreset) (storyblok.Component, error) {
	defaultName := defaultPresetName(updated, presets)
	updated.ID = existing.ID
	updated.PresetID = 0

	resultComponent, err := client.UpdateComponent(ctx, spaceID, existing.ID, updated)
	if err != nil {
		return storyblok.Component{}, err
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
				return storyblok.Component{}, err
			}
			existingPresets[key] = updatedPreset
		} else {
			preset.ID = 0
			createdPreset, err := client.CreatePreset(ctx, spaceID, preset)
			if err != nil {
				return storyblok.Component{}, err
			}
			existingPresets[key] = createdPreset
		}
	}

	if defaultName != "" {
		if targetPreset, ok := existingPresets[defaultName]; ok {
			if targetPreset.ID != existing.PresetID {
				updated.PresetID = targetPreset.ID
				if refreshed, err := client.UpdateComponent(ctx, spaceID, existing.ID, updated); err != nil {
					return storyblok.Component{}, err
				} else {
					resultComponent = refreshed
				}
			}
		}
	}

	return resultComponent, nil
}

func printPushSummary(result Result, opts Options) {
	if opts.DryRun {
		fmt.Println()
		fmt.Printf("Dry run summary: %d components, %d presets (rate-limit retries: %d, server retries: %d)\n",
			result.ComponentsSynced,
			result.PresetsSynced,
			result.RateLimitRetries,
			result.ServerErrorRetries,
		)
		if len(result.MissingSelectors) > 0 {
			fmt.Fprintf(os.Stderr, "Missing components matching: %s\n", strings.Join(result.MissingSelectors, ", "))
		}
		return
	}

	fmt.Println()
	fmt.Printf("Pushed %d components and %d presets to space %d in %s (rate-limit retries: %d, server retries: %d)\n",
		result.ComponentsSynced,
		result.PresetsSynced,
		opts.SpaceID,
		result.Duration.Truncate(time.Millisecond),
		result.RateLimitRetries,
		result.ServerErrorRetries,
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

func summarizeList(items []string, limit int) string {
	if len(items) == 0 {
		return ""
	}
	if limit <= 0 {
		limit = 3
	}
	if len(items) <= limit {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(items[:limit], ", "), len(items)-limit)
}
