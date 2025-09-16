package storyblok

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"strings"

	"sbx/internal/infra/limiter"
)

const (
	defaultBaseURL   = "https://mapi.storyblok.com/v1"
	defaultUserAgent = "sbx-cli"
)

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets a custom http.Client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithBaseURL overrides the default Storyblok base URL.
func WithBaseURL(rawURL string) Option {
	return func(c *Client) {
		if rawURL != "" {
			c.baseURL = rawURL
		}
	}
}

// WithLimiter attaches a limiter for rate control.
func WithLimiter(l *limiter.SpaceLimiter) Option {
	return func(c *Client) {
		c.limiter = l
	}
}

// Client performs Storyblok Management API requests.
type Client struct {
	httpClient *http.Client
	baseURL    string
	token      string
	userAgent  string
	limiter    *limiter.SpaceLimiter

	maxRetries   int
	backoffStart time.Duration
}

// NewClient constructs a Storyblok API client.
func NewClient(token string, opts ...Option) *Client {
	client := &Client{
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		baseURL:      defaultBaseURL,
		token:        token,
		userAgent:    defaultUserAgent,
		maxRetries:   5,
		backoffStart: 250 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(client)
	}
	return client
}

func (c *Client) cloneWithToken(token string) *Client {
	clone := *c
	clone.token = token
	return &clone
}

// WithToken returns a shallow copy with a new token.
func (c *Client) WithToken(token string) *Client {
	return c.cloneWithToken(token)
}

type requestArgs struct {
	method  string
	path    string
	query   url.Values
	spaceID int
	payload any
	out     any
	isWrite bool
}

func buildAuthHeader(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	return token
}

func (c *Client) do(ctx context.Context, args requestArgs) error {
	if c.token == "" {
		return fmt.Errorf("storyblok client requires a token")
	}

	var payload []byte
	var err error
	if args.payload != nil {
		payload, err = json.Marshal(args.payload)
		if err != nil {
			return err
		}
	}

	backoff := c.backoffStart
	var lastErr error

	for attempt := 0; attempt < c.maxRetries; attempt++ {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}

		reqURL := c.baseURL + args.path
		if len(args.query) > 0 {
			reqURL = fmt.Sprintf("%s?%s", reqURL, args.query.Encode())
		}

		req, err := http.NewRequestWithContext(ctx, args.method, reqURL, body)
		if err != nil {
			return err
		}

		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)
		authHeader := buildAuthHeader(c.token)
		if authHeader == "" {
			return fmt.Errorf("storyblok client requires a token")
		}
		req.Header.Set("Authorization", authHeader)
		if args.payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		if c.limiter != nil {
			if args.isWrite {
				if err := c.limiter.WaitWrite(ctx, args.spaceID); err != nil {
					return err
				}
			} else {
				if err := c.limiter.WaitRead(ctx, args.spaceID); err != nil {
					return err
				}
			}
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			select {
			case <-time.After(backoff):
				backoff *= 2
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		func() {
			defer resp.Body.Close()

			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				if args.out != nil {
					decoder := json.NewDecoder(resp.Body)
					if err := decoder.Decode(args.out); err != nil && err != io.EOF {
						lastErr = err
						return
					}
				}

				if c.limiter != nil {
					if args.isWrite {
						c.limiter.NudgeWrite(args.spaceID, +0.02, 1, 7)
					} else {
						c.limiter.NudgeRead(args.spaceID, +0.02, 1, 7)
					}
				}

				lastErr = nil
				return
			}

			responseBody, _ := io.ReadAll(resp.Body)
			err := &APIError{
				StatusCode: resp.StatusCode,
				Body:       responseBody,
				Message:    decodeErrorMessage(responseBody),
			}
			lastErr = err

			if IsRateLimited(err) {
				if c.limiter != nil {
					if args.isWrite {
						c.limiter.NudgeWrite(args.spaceID, -0.2, 1, 7)
					} else {
						c.limiter.NudgeRead(args.spaceID, -0.2, 1, 7)
					}
				}
				retry := CountersFromContext(ctx)
				if retry != nil {
					retry.RecordRateLimit()
				}
				return
			}

			if resp.StatusCode >= 500 {
				retry := CountersFromContext(ctx)
				if retry != nil {
					retry.RecordServerError()
				}
				return
			}

			// Non-retriable error
			lastErr = err
			backoff = 0
		}()

		if lastErr == nil {
			return nil
		}

		if apiErr, ok := lastErr.(*APIError); ok {
			if apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 && apiErr.StatusCode != http.StatusTooManyRequests {
				return apiErr
			}
		}

		if backoff == 0 {
			break
		}

		select {
		case <-time.After(backoff):
			backoff *= 2
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return lastErr
}

// DeletePreset removes a preset by ID.
func (c *Client) DeletePreset(ctx context.Context, spaceID, presetID int) error {
	return c.do(ctx, requestArgs{
		method:  http.MethodDelete,
		path:    fmt.Sprintf("/spaces/%d/presets/%d", spaceID, presetID),
		spaceID: spaceID,
		isWrite: true,
	})
}

// ListComponents retrieves all components for a space.
func (c *Client) ListComponents(ctx context.Context, spaceID int) ([]Component, error) {
	var response struct {
		Components []Component `json:"components"`
	}
	if err := c.do(ctx, requestArgs{
		method:  http.MethodGet,
		path:    fmt.Sprintf("/spaces/%d/components", spaceID),
		spaceID: spaceID,
		out:     &response,
	}); err != nil {
		return nil, err
	}
	return response.Components, nil
}

// GetComponent fetches a component by ID.
func (c *Client) GetComponent(ctx context.Context, spaceID, componentID int) (Component, error) {
	var response struct {
		Component Component `json:"component"`
	}
	if err := c.do(ctx, requestArgs{
		method:  http.MethodGet,
		path:    fmt.Sprintf("/spaces/%d/components/%d", spaceID, componentID),
		spaceID: spaceID,
		out:     &response,
	}); err != nil {
		return Component{}, err
	}
	return response.Component, nil
}

// CreateComponent creates a component.
func (c *Client) CreateComponent(ctx context.Context, spaceID int, component Component) (Component, error) {
	var response struct {
		Component Component `json:"component"`
	}
	payload := map[string]any{"component": component}
	if err := c.do(ctx, requestArgs{
		method:  http.MethodPost,
		path:    fmt.Sprintf("/spaces/%d/components", spaceID),
		spaceID: spaceID,
		payload: payload,
		out:     &response,
		isWrite: true,
	}); err != nil {
		return Component{}, err
	}
	return response.Component, nil
}

// UpdateComponent updates an existing component.
func (c *Client) UpdateComponent(ctx context.Context, spaceID, componentID int, component Component) (Component, error) {
	var response struct {
		Component Component `json:"component"`
	}
	payload := map[string]any{"component": component}
	if err := c.do(ctx, requestArgs{
		method:  http.MethodPut,
		path:    fmt.Sprintf("/spaces/%d/components/%d", spaceID, componentID),
		spaceID: spaceID,
		payload: payload,
		out:     &response,
		isWrite: true,
	}); err != nil {
		return Component{}, err
	}
	return response.Component, nil
}

// ListComponentGroups fetches component groups.
func (c *Client) ListComponentGroups(ctx context.Context, spaceID int) ([]ComponentGroup, error) {
	var response struct {
		ComponentGroups []ComponentGroup `json:"component_groups"`
	}
	if err := c.do(ctx, requestArgs{
		method:  http.MethodGet,
		path:    fmt.Sprintf("/spaces/%d/component_groups", spaceID),
		spaceID: spaceID,
		out:     &response,
	}); err != nil {
		return nil, err
	}
	return response.ComponentGroups, nil
}

// CreateComponentGroup creates a new component group.
func (c *Client) CreateComponentGroup(ctx context.Context, spaceID int, group ComponentGroup) (ComponentGroup, error) {
	var response struct {
		ComponentGroup ComponentGroup `json:"component_group"`
	}
	payload := map[string]any{"component_group": group}
	if err := c.do(ctx, requestArgs{
		method:  http.MethodPost,
		path:    fmt.Sprintf("/spaces/%d/component_groups", spaceID),
		spaceID: spaceID,
		payload: payload,
		out:     &response,
		isWrite: true,
	}); err != nil {
		return ComponentGroup{}, err
	}
	return response.ComponentGroup, nil
}

// ListPresets returns presets for a space.
func (c *Client) ListPresets(ctx context.Context, spaceID int) ([]ComponentPreset, error) {
	var response struct {
		Presets []ComponentPreset `json:"presets"`
	}
	if err := c.do(ctx, requestArgs{
		method:  http.MethodGet,
		path:    fmt.Sprintf("/spaces/%d/presets", spaceID),
		spaceID: spaceID,
		out:     &response,
	}); err != nil {
		return nil, err
	}
	return response.Presets, nil
}

// CreatePreset creates a preset.
func (c *Client) CreatePreset(ctx context.Context, spaceID int, preset ComponentPreset) (ComponentPreset, error) {
	var response struct {
		Preset ComponentPreset `json:"preset"`
	}
	payload := map[string]any{"preset": preset}
	if err := c.do(ctx, requestArgs{
		method:  http.MethodPost,
		path:    fmt.Sprintf("/spaces/%d/presets", spaceID),
		spaceID: spaceID,
		payload: payload,
		out:     &response,
		isWrite: true,
	}); err != nil {
		return ComponentPreset{}, err
	}
	return response.Preset, nil
}

// UpdatePreset updates an existing preset.
func (c *Client) UpdatePreset(ctx context.Context, spaceID int, preset ComponentPreset) (ComponentPreset, error) {
	if preset.ID == 0 {
		return ComponentPreset{}, fmt.Errorf("preset ID is required for update")
	}
	var response struct {
		Preset ComponentPreset `json:"preset"`
	}
	payload := map[string]any{"preset": preset}
	if err := c.do(ctx, requestArgs{
		method:  http.MethodPut,
		path:    fmt.Sprintf("/spaces/%d/presets/%d", spaceID, preset.ID),
		spaceID: spaceID,
		payload: payload,
		out:     &response,
		isWrite: true,
	}); err != nil {
		return ComponentPreset{}, err
	}
	return response.Preset, nil
}

// ListInternalTags retrieves internal tags for a space.
func (c *Client) ListInternalTags(ctx context.Context, spaceID int) ([]InternalTag, error) {
	var response struct {
		InternalTags []InternalTag `json:"internal_tags"`
	}
	if err := c.do(ctx, requestArgs{
		method:  http.MethodGet,
		path:    fmt.Sprintf("/spaces/%d/internal_tags", spaceID),
		spaceID: spaceID,
		out:     &response,
	}); err != nil {
		return nil, err
	}
	return response.InternalTags, nil
}

// CreateInternalTag creates an internal tag for a component.
func (c *Client) CreateInternalTag(ctx context.Context, spaceID int, tag InternalTag) (InternalTag, error) {
	var response struct {
		InternalTag InternalTag `json:"internal_tag"`
	}
	payload := map[string]any{"internal_tag": tag}
	if err := c.do(ctx, requestArgs{
		method:  http.MethodPost,
		path:    fmt.Sprintf("/spaces/%d/internal_tags", spaceID),
		spaceID: spaceID,
		payload: payload,
		out:     &response,
		isWrite: true,
	}); err != nil {
		return InternalTag{}, err
	}
	return response.InternalTag, nil
}

// GetSpaceOptions fetches general space configuration such as languages.
func (c *Client) GetSpaceOptions(ctx context.Context, spaceID int) (SpaceOptions, error) {
	var response struct {
		Space SpaceOptions `json:"space"`
	}
	if err := c.do(ctx, requestArgs{
		method:  http.MethodGet,
		path:    fmt.Sprintf("/spaces/%d", spaceID),
		spaceID: spaceID,
		out:     &response,
	}); err != nil {
		return SpaceOptions{}, err
	}
	return response.Space, nil
}
