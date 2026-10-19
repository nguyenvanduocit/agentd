package home

import (
	"math"
	"math/rand/v2"
	"time"
)

// BackoffOptions shapes BackoffDelay.
type BackoffOptions struct {
	Base time.Duration
	Max  time.Duration
	// Jitter is 0..1: the share of each delay that is random, so daemons that lost one server do not return in lockstep.
	Jitter float64
}

// BackoffDelay is the delay before retry number `attempt` (0 is the first retry): exponential, capped, jittered
// downwards, in whole milliseconds. random returns 0..1; nil means math/rand/v2.
func BackoffDelay(attempt int, o BackoffOptions, random func() float64) time.Duration {
	if random == nil {
		random = rand.Float64
	}
	baseMs := float64(o.Base / time.Millisecond)
	maxMs := float64(o.Max / time.Millisecond)
	exp := math.Min(maxMs, baseMs*math.Pow(2, float64(min(attempt, 30))))
	return time.Duration(math.Round(exp*(1-o.Jitter*random()))) * time.Millisecond
}
