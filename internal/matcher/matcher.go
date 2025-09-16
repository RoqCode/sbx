package matcher

import (
	"fmt"
	"path/filepath"
	"strings"
)

const (
	ModeExact  = "exact"
	ModePrefix = "prefix"
	ModeGlob   = "glob"
)

var validModes = map[string]struct{}{
	ModeExact:  {},
	ModePrefix: {},
	ModeGlob:   {},
}

// ValidateMode ensures a provided mode is supported.
func ValidateMode(mode string) error {
	mode = strings.ToLower(mode)
	if _, ok := validModes[mode]; ok {
		return nil
	}
	return fmt.Errorf("invalid match mode %q (expected exact, prefix, or glob)", mode)
}

// Filter selects items whose name satisfies any of the provided selectors.
// When all is true, all items are returned and missing selectors is empty.
func Filter[T any](items []T, nameFn func(T) string, selectors []string, mode string, all bool) (selected []T, missing []string, err error) {
	mode = strings.ToLower(mode)
	if err := ValidateMode(mode); err != nil {
		return nil, nil, err
	}

	if all {
		selected = make([]T, len(items))
		copy(selected, items)
		return selected, nil, nil
	}

	if len(selectors) == 0 {
		return nil, nil, fmt.Errorf("no selectors provided")
	}

	// normalise selectors for comparisons
	normSelectors := make([]string, len(selectors))
	for i, s := range selectors {
		normSelectors[i] = strings.ToLower(strings.TrimSpace(s))
	}

	hit := make([]bool, len(normSelectors))
	seen := make(map[string]struct{})

	for _, item := range items {
		name := nameFn(item)
		cmp := strings.ToLower(name)

		match := false
		for i, selector := range normSelectors {
			if selector == "" {
				continue
			}
			if matches(cmp, selector, mode) {
				hit[i] = true
				match = true
			}
		}
		if match {
			if _, exists := seen[name]; !exists {
				selected = append(selected, item)
				seen[name] = struct{}{}
			}
		}
	}

	for i, ok := range hit {
		if !ok {
			missing = append(missing, selectors[i])
		}
	}

	return selected, missing, nil
}

func matches(value, selector, mode string) bool {
	switch mode {
	case ModeExact:
		return value == selector
	case ModePrefix:
		return strings.HasPrefix(value, selector)
	case ModeGlob:
		ok, err := filepath.Match(selector, value)
		return err == nil && ok
	default:
		return false
	}
}
