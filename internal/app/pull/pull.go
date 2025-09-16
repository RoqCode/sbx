package pull

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"sbx/internal/fsutil"
	"sbx/internal/infra/limiter"
	"sbx/internal/matcher"
	"sbx/internal/storyblok"
)

// Options collects configuration for pull operations.
type Options struct {
	Token     string
	SpaceID   int
	Names     []string
	MatchMode string
	All       bool
	OutDir    string
	DryRun    bool
}

// Result captures a high-level summary for reporting/exit codes.
type Result struct {
	ExitCode         int
	ComponentsSynced int
	PresetsSynced    int
	Duration         time.Duration
	RateLimitRetries int64
	MissingSelectors []string
}

// Run executes the pull workflow.
func Run(ctx context.Context, opts Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	result := Result{ExitCode: 0}

	if err := matcher.ValidateMode(opts.MatchMode); err != nil {
		return result, err
	}

	if !opts.All && len(opts.Names) == 0 {
		return result, fmt.Errorf("no component names provided; use --all to pull every component")
	}

	start := time.Now()

	lim := limiter.NewSpaceLimiter(7, 7, 7)
	client := storyblok.NewClient(opts.Token, storyblok.WithLimiter(lim))

	counters := &storyblok.RetryCounters{}
	ctx = storyblok.WithRetryCounters(ctx, counters)

	var components []storyblok.Component
	var groups []storyblok.ComponentGroup
	var presets []storyblok.ComponentPreset

	eg, egCtx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		list, err := client.ListComponents(egCtx, opts.SpaceID)
		if err != nil {
			return err
		}
		components = list
		return nil
	})
	eg.Go(func() error {
		list, err := client.ListComponentGroups(egCtx, opts.SpaceID)
		if err != nil {
			return err
		}
		groups = list
		return nil
	})
	eg.Go(func() error {
		list, err := client.ListPresets(egCtx, opts.SpaceID)
		if err != nil {
			return err
		}
		presets = list
		return nil
	})

	if err := eg.Wait(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to load data from Storyblok: %v\n", err)
		result.ExitCode = 2
		return result, err
	}

	groupNameByUUID := make(map[string]string, len(groups))
	for _, g := range groups {
		if g.UUID != "" {
			groupNameByUUID[g.UUID] = g.Name
		}
	}

	for i := range components {
		uuid := components[i].ComponentGroupUUID
		if uuid == "" {
			continue
		}
		if name, ok := groupNameByUUID[uuid]; ok {
			components[i].ComponentGroupName = name
		}
	}

	selectedComponents, missing, err := matcher.Filter(components, func(c storyblok.Component) string {
		return c.Name
	}, opts.Names, opts.MatchMode, opts.All)
	if err != nil {
		return result, err
	}

	result.MissingSelectors = missing
	if len(missing) > 0 {
		result.ExitCode = 1
	}

	selectedPresets := filterPresetsForComponents(presets, selectedComponents)

	actions := buildPullActions(opts.SpaceID, opts.OutDir, selectedComponents, selectedPresets)

	if opts.DryRun {
		printDryRun(actions, opts.SpaceID)
	} else {
		if err := executePull(actions); err != nil {
			result.ExitCode = 2
			return result, err
		}
	}

	dur := time.Since(start)
	result.ComponentsSynced = len(selectedComponents)
	result.PresetsSynced = len(selectedPresets)
	result.Duration = dur
	result.RateLimitRetries = counters.Status429.Load()

	printSummary(result, opts)

	return result, nil
}

func filterPresetsForComponents(presets []storyblok.ComponentPreset, components []storyblok.Component) []storyblok.ComponentPreset {
	componentIDs := make(map[int]struct{})
	componentNames := make(map[string]struct{})
	for _, comp := range components {
		if comp.ID != 0 {
			componentIDs[comp.ID] = struct{}{}
		}
		componentNames[strings.ToLower(comp.Name)] = struct{}{}
	}

	var matched []storyblok.ComponentPreset
	for _, preset := range presets {
		_, idMatch := componentIDs[preset.ComponentID]
		var nameMatch bool
		if compName, ok := preset.Preset["component"].(string); ok {
			_, nameMatch = componentNames[strings.ToLower(compName)]
		}
		if idMatch || nameMatch {
			matched = append(matched, preset)
		}
	}
	return matched
}

// pullAction describes a single file action performed by pull.
type pullAction struct {
	Kind       string
	Name       string
	OutputPath string
	Overwrite  bool
	Payload    any
}

func buildPullActions(spaceID int, outDir string, components []storyblok.Component, presets []storyblok.ComponentPreset) []pullAction {
	var actions []pullAction
	for _, component := range components {
		filename := fmt.Sprintf("%s-%d.json", component.Name, spaceID)
		path := filepath.Join(outDir, filename)
		overwrite, _ := fsutil.Exists(path)
		actions = append(actions, pullAction{
			Kind:       "component",
			Name:       component.Name,
			OutputPath: path,
			Overwrite:  overwrite,
			Payload:    component,
		})
	}
	for _, preset := range presets {
		filename := fmt.Sprintf("%s-%d.json", preset.Name, spaceID)
		path := filepath.Join(outDir, filename)
		overwrite, _ := fsutil.Exists(path)
		actions = append(actions, pullAction{
			Kind:       "preset",
			Name:       preset.Name,
			OutputPath: path,
			Overwrite:  overwrite,
			Payload:    preset,
		})
	}
	return actions
}

func printDryRun(actions []pullAction, spaceID int) {
	fmt.Printf("Dry run: pulling from space %d\n", spaceID)
	for _, action := range actions {
		verb := "create"
		if action.Overwrite {
			verb = "overwrite"
		}
		fmt.Printf("  - %s %s -> %s (%s)\n", action.Kind, action.Name, action.OutputPath, verb)
	}
}

func executePull(actions []pullAction) error {
	for _, action := range actions {
		if err := fsutil.WriteJSON(action.OutputPath, action.Payload, 0); err != nil {
			return err
		}
		fmt.Printf("Saved %s %s to %s\n", action.Kind, action.Name, action.OutputPath)
	}
	return nil
}

func printSummary(result Result, opts Options) {
	if opts.DryRun {
		fmt.Println()
		fmt.Printf("Dry run summary: %d components, %d presets (rate-limit retries: %d)\n",
			result.ComponentsSynced, result.PresetsSynced, result.RateLimitRetries)
		if len(result.MissingSelectors) > 0 {
			fmt.Fprintf(os.Stderr, "Missing components matching: %s\n", strings.Join(result.MissingSelectors, ", "))
		}
		return
	}

	fmt.Println()
	fmt.Printf("Pulled %d components and %d presets from space %d in %s (rate-limit retries: %d)\n",
		result.ComponentsSynced,
		result.PresetsSynced,
		opts.SpaceID,
		result.Duration.Truncate(time.Millisecond),
		result.RateLimitRetries,
	)
	if len(result.MissingSelectors) > 0 {
		fmt.Fprintf(os.Stderr, "Missing components matching: %s\n", strings.Join(result.MissingSelectors, ", "))
	}
}
