package storyblok

import (
	"encoding/json"
	"net/http"
)

// APIError wraps HTTP status codes and response messages from Storyblok.
type APIError struct {
	StatusCode int
	Message    string
	Body       []byte
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return http.StatusText(e.StatusCode)
}

// decodeErrorMessage attempts to extract a useful message from the API response body.
func decodeErrorMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	if msg, ok := envelope["message"].(string); ok {
		return msg
	}
	if errObj, ok := envelope["error"].(map[string]any); ok {
		if msg, ok := errObj["message"].(string); ok {
			return msg
		}
	}
	return ""
}

// IsNotFound reports whether the error is a 404.
func IsNotFound(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.StatusCode == http.StatusNotFound
	}
	return false
}

// IsUnauthorized reports whether the error is a 401.
func IsUnauthorized(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.StatusCode == http.StatusUnauthorized
	}
	return false
}

// IsRateLimited reports whether the error indicates rate limiting.
func IsRateLimited(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.StatusCode == http.StatusTooManyRequests
	}
	return false
}

// IsValidationError reports whether the error is a 4xx validation failure (422).
func IsValidationError(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.StatusCode == http.StatusUnprocessableEntity
	}
	return false
}
