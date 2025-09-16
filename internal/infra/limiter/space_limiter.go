package limiter

import (
	"context"
	"sync"
	"time"
)

// SpaceLimiter provides per-space token buckets for read/write operations.
type SpaceLimiter struct {
	mu     sync.Mutex
	spaces map[int]*spaceBuckets

	defReadRPS  float64
	defWriteRPS float64
	burst       float64
}

type spaceBuckets struct {
	read  *tokenBucket
	write *tokenBucket
}

type tokenBucket struct {
	mu     sync.Mutex
	rps    float64
	burst  float64
	tokens float64
	last   time.Time
}

// NewSpaceLimiter constructs a limiter with the provided defaults.
func NewSpaceLimiter(readRPS, writeRPS float64, burst int) *SpaceLimiter {
	if readRPS <= 0 {
		readRPS = 7
	}
	if writeRPS <= 0 {
		writeRPS = 7
	}
	if burst <= 0 {
		burst = 7
	}

	return &SpaceLimiter{
		spaces:      make(map[int]*spaceBuckets),
		defReadRPS:  readRPS,
		defWriteRPS: writeRPS,
		burst:       float64(burst),
	}
}

func newBucket(rps, burst float64) *tokenBucket {
	now := time.Now()
	return &tokenBucket{
		rps:    rps,
		burst:  burst,
		tokens: burst,
		last:   now,
	}
}

func (sl *SpaceLimiter) get(spaceID int) *spaceBuckets {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	bucket, ok := sl.spaces[spaceID]
	if ok {
		return bucket
	}

	bucket = &spaceBuckets{
		read:  newBucket(sl.defReadRPS, sl.burst),
		write: newBucket(sl.defWriteRPS, sl.burst),
	}

	sl.spaces[spaceID] = bucket
	return bucket
}

// WaitRead blocks until a read token is available for the given space.
func (sl *SpaceLimiter) WaitRead(ctx context.Context, spaceID int) error {
	return sl.get(spaceID).read.wait(ctx)
}

// WaitWrite blocks until a write token is available for the given space.
func (sl *SpaceLimiter) WaitWrite(ctx context.Context, spaceID int) error {
	return sl.get(spaceID).write.wait(ctx)
}

// NudgeRead adjusts the read RPS gradually within [min,max].
func (sl *SpaceLimiter) NudgeRead(spaceID int, delta, min, max float64) {
	sl.nudge(sl.get(spaceID).read, delta, min, max)
}

// NudgeWrite adjusts the write RPS gradually within [min,max].
func (sl *SpaceLimiter) NudgeWrite(spaceID int, delta, min, max float64) {
	sl.nudge(sl.get(spaceID).write, delta, min, max)
}

func (sl *SpaceLimiter) nudge(b *tokenBucket, delta, min, max float64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	r := b.rps + delta
	if r < min {
		r = min
	}
	if r > max {
		r = max
	}
	b.rps = r
}

func (b *tokenBucket) wait(ctx context.Context) error {
	for {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}

		b.mu.Lock()
		now := time.Now()
		b.refillLocked(now)
		if b.tokens >= 1 {
			b.tokens -= 1
			b.mu.Unlock()
			return nil
		}
		needed := 1 - b.tokens
		rps := b.rps
		b.mu.Unlock()

		if rps <= 0 {
			rps = 1
		}

		wait := time.Duration((needed / rps) * float64(time.Second))
		if wait < 5*time.Millisecond {
			wait = 5 * time.Millisecond
		}

		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
}

func (b *tokenBucket) refillLocked(now time.Time) {
	delta := now.Sub(b.last).Seconds() * b.rps
	if delta <= 0 {
		return
	}
	b.tokens += delta
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now
}

// DefaultLimitsForPlan returns recommended limiter values based on plan level.
func DefaultLimitsForPlan(planLevel int) (readRPS, writeRPS float64, burst int) {
	if planLevel <= 0 {
		return 4, 3, 3
	}
	return 7, 7, 7
}

type Counters struct {
	ReadRequests  int64
	WriteRequests int64
}

// SnapshotCounters reports an approximate total of tokens consumed for a space.
func (sl *SpaceLimiter) SnapshotCounters(spaceID int) Counters {
	bucket := sl.get(spaceID)
	bucket.read.mu.Lock()
	defer bucket.read.mu.Unlock()
	bucket.write.mu.Lock()
	defer bucket.write.mu.Unlock()

	readElapsed := time.Since(bucket.read.last).Seconds()
	writeElapsed := time.Since(bucket.write.last).Seconds()

	return Counters{
		ReadRequests:  int64(bucket.read.rps * readElapsed),
		WriteRequests: int64(bucket.write.rps * writeElapsed),
	}
}
