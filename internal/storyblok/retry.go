package storyblok

import (
	"context"
	"sync/atomic"
)

// RetryCounters tracks rate-limit and retry statistics for reporting.
type RetryCounters struct {
	Status429 atomic.Int64
	Status5xx atomic.Int64
	Total     atomic.Int64
}

type retryKey struct{}

// WithRetryCounters attaches counters to a context for downstream tracking.
func WithRetryCounters(ctx context.Context, counters *RetryCounters) context.Context {
	return context.WithValue(ctx, retryKey{}, counters)
}

// CountersFromContext fetches retry counters from a context.
func CountersFromContext(ctx context.Context) *RetryCounters {
	if v := ctx.Value(retryKey{}); v != nil {
		if counters, ok := v.(*RetryCounters); ok {
			return counters
		}
	}
	return nil
}

// RecordRateLimit increments counters for rate-limited responses.
func (r *RetryCounters) RecordRateLimit() {
	if r == nil {
		return
	}
	r.Status429.Add(1)
	r.Total.Add(1)
}

// RecordServerError increments counters for server errors.
func (r *RetryCounters) RecordServerError() {
	if r == nil {
		return
	}
	r.Status5xx.Add(1)
	r.Total.Add(1)
}
