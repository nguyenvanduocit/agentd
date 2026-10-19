package home

import (
	"testing"
	"time"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestBackoffDelayDoublesUpToTheCap(t *testing.T) {
	o := BackoffOptions{Base: ms(500), Max: ms(15_000)}
	want := []time.Duration{ms(500), ms(1000), ms(2000), ms(4000), ms(8000), ms(15_000), ms(15_000), ms(15_000)}
	for i, attempt := range []int{0, 1, 2, 3, 4, 5, 6, 40} {
		if got := BackoffDelay(attempt, o, nil); got != want[i] {
			t.Errorf("BackoffDelay(%d) = %v, want %v", attempt, got, want[i])
		}
	}
}

func TestBackoffDelayJitterOnlyShortensTheDelayByAtMostItsShare(t *testing.T) {
	o := BackoffOptions{Base: ms(500), Max: ms(15_000), Jitter: 0.5}
	for _, tc := range []struct {
		random float64
		want   time.Duration
	}{{0, ms(2000)}, {1, ms(1000)}, {0.5, ms(1500)}} {
		if got := BackoffDelay(2, o, func() float64 { return tc.random }); got != tc.want {
			t.Errorf("random %v: BackoffDelay = %v, want %v", tc.random, got, tc.want)
		}
	}
}

// The adapter installer retries from 60 s, doubling up to 30 min (daemon/test/unit/adapters.test.ts "retries start at 60 s").
func TestBackoffDelayAdapterRetryShape(t *testing.T) {
	o := BackoffOptions{Base: 60 * time.Second, Max: 30 * time.Minute}
	want := []int{60, 120, 240, 480, 960, 1800, 1800, 1800, 1800}
	for i, attempt := range []int{0, 1, 2, 3, 4, 5, 6, 7, 20} {
		if got := BackoffDelay(attempt, o, nil); got != time.Duration(want[i])*time.Second {
			t.Errorf("BackoffDelay(%d) = %v, want %ds", attempt, got, want[i])
		}
	}
}

func TestBackoffDelayDefaultRandomStaysInRange(t *testing.T) {
	o := BackoffOptions{Base: ms(1000), Max: ms(1000), Jitter: 0.5}
	for range 100 {
		if got := BackoffDelay(0, o, nil); got < ms(500) || got > ms(1000) {
			t.Fatalf("BackoffDelay = %v, want 500ms..1s", got)
		}
	}
}
