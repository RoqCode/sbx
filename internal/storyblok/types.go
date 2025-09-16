package storyblok

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Component models a Storyblok component. Unknown fields are preserved round-trip via Extras.
type Component struct {
	ID                 int               `json:"id,omitempty"`
	Name               string            `json:"name"`
	DisplayName        string            `json:"display_name,omitempty"`
	Schema             map[string]any    `json:"schema"`
	ComponentGroupUUID string            `json:"component_group_uuid,omitempty"`
	ComponentGroupName string            `json:"component_group_name,omitempty"`
	PresetID           int               `json:"preset_id,omitempty"`
	InternalTagsList   []InternalTag     `json:"internal_tags_list,omitempty"`
	InternalTagIDs     IntSlice          `json:"internal_tag_ids,omitempty"`
	AllPresets         []ComponentPreset `json:"all_presets,omitempty"`
	Extras             map[string]any    `json:"-"`
}

// UnmarshalJSON preserves unknown fields in Extras.
func (c *Component) UnmarshalJSON(data []byte) error {
	type alias Component
	tmp := alias{}
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	exclude := map[string]struct{}{
		"id":                   {},
		"name":                 {},
		"display_name":         {},
		"schema":               {},
		"component_group_uuid": {},
		"component_group_name": {},
		"preset_id":            {},
		"internal_tags_list":   {},
		"internal_tag_ids":     {},
		"all_presets":          {},
	}

	extra := make(map[string]any)
	for k, v := range raw {
		if _, ok := exclude[k]; !ok {
			extra[k] = v
		}
	}

	*c = Component(tmp)
	c.Extras = extra
	return nil
}

// MarshalJSON re-applies Extras on output.
func (c Component) MarshalJSON() ([]byte, error) {
	type alias Component
	tmp := alias(c)
	data, err := json.Marshal(tmp)
	if err != nil {
		return nil, err
	}
	if len(c.Extras) == 0 {
		return data, nil
	}

	var base map[string]any
	if err := json.Unmarshal(data, &base); err != nil {
		return nil, err
	}

	for k, v := range c.Extras {
		base[k] = v
	}

	return json.Marshal(base)
}

// IntSlice handles Storyblok arrays that may contain numeric strings.
type IntSlice []int

// UnmarshalJSON accepts numbers or numeric strings and gracefully handles empty strings.
func (s *IntSlice) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = nil
		return nil
	}
	var raw []any
	if err := json.Unmarshal(data, &raw); err != nil {
		var single string
		if err2 := json.Unmarshal(data, &single); err2 == nil {
			if strings.TrimSpace(single) == "" {
				*s = nil
				return nil
			}
		}
		return err
	}
	ints := make([]int, 0, len(raw))
	for _, entry := range raw {
		switch v := entry.(type) {
		case float64:
			ints = append(ints, int(v))
		case string:
			if strings.TrimSpace(v) == "" {
				continue
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid integer value %q: %w", v, err)
			}
			ints = append(ints, n)
		case nil:
			continue
		default:
			return fmt.Errorf("unsupported value type %T in IntSlice", entry)
		}
	}
	*s = IntSlice(ints)
	return nil
}

// MarshalJSON outputs standard integer arrays.
func (s IntSlice) MarshalJSON() ([]byte, error) {
	ints := make([]int, len(s))
	copy(ints, s)
	return json.Marshal(ints)
}

// ComponentGroup represents a Storyblok component group.
type ComponentGroup struct {
	ID         int    `json:"id,omitempty"`
	UUID       string `json:"uuid,omitempty"`
	Name       string `json:"name"`
	ParentID   *int   `json:"parent_id,omitempty"`
	SourceUUID string `json:"source_uuid,omitempty"`
}

// InternalTag describes an internal tag associated with components.
type InternalTag struct {
	ID         int    `json:"id,omitempty"`
	Name       string `json:"name"`
	ObjectType string `json:"object_type,omitempty"`
}

// ComponentPreset models component presets.
type ComponentPreset struct {
	ID          int            `json:"id,omitempty"`
	Name        string         `json:"name"`
	ComponentID int            `json:"component_id,omitempty"`
	Preset      map[string]any `json:"preset"`
	Image       any            `json:"image,omitempty"`
	Extras      map[string]any `json:"-"`
}

// UnmarshalJSON keeps extra fields for presets.
func (p *ComponentPreset) UnmarshalJSON(data []byte) error {
	type alias ComponentPreset
	tmp := alias{}
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	exclude := map[string]struct{}{
		"id":           {},
		"name":         {},
		"component_id": {},
		"preset":       {},
		"image":        {},
	}

	extra := make(map[string]any)
	for k, v := range raw {
		if _, ok := exclude[k]; !ok {
			extra[k] = v
		}
	}

	*p = ComponentPreset(tmp)
	p.Extras = extra
	return nil
}

func (p ComponentPreset) MarshalJSON() ([]byte, error) {
	type alias ComponentPreset
	tmp := alias(p)
	data, err := json.Marshal(tmp)
	if err != nil {
		return nil, err
	}

	if len(p.Extras) == 0 {
		return data, nil
	}

	var base map[string]any
	if err := json.Unmarshal(data, &base); err != nil {
		return nil, err
	}

	for k, v := range p.Extras {
		base[k] = v
	}

	return json.Marshal(base)
}

// SpaceOptions captures info like languages from the space.
type SpaceOptions struct {
	ID              int            `json:"id"`
	Name            string         `json:"name"`
	PlanLevel       int            `json:"plan_level"`
	DefaultLang     string         `json:"default_lang"`
	DefaultLangName string         `json:"default_lang_name"`
	Languages       []LangOption   `json:"languages"`
	Extras          map[string]any `json:"-"`
}

// LangOption describes a language entry in space options.
type LangOption struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	IsDefault bool   `json:"is_default"`
}

func (s *SpaceOptions) UnmarshalJSON(data []byte) error {
	type alias SpaceOptions
	tmp := alias{}
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	exclude := map[string]struct{}{
		"id":                {},
		"name":              {},
		"plan_level":        {},
		"default_lang":      {},
		"default_lang_name": {},
		"languages":         {},
	}

	extra := make(map[string]any)
	for k, v := range raw {
		if _, ok := exclude[k]; !ok {
			extra[k] = v
		}
	}

	*s = SpaceOptions(tmp)
	s.Extras = extra
	return nil
}

func (s SpaceOptions) MarshalJSON() ([]byte, error) {
	type alias SpaceOptions
	tmp := alias(s)
	data, err := json.Marshal(tmp)
	if err != nil {
		return nil, err
	}
	if len(s.Extras) == 0 {
		return data, nil
	}

	var base map[string]any
	if err := json.Unmarshal(data, &base); err != nil {
		return nil, err
	}

	for k, v := range s.Extras {
		base[k] = v
	}

	return json.Marshal(base)
}
