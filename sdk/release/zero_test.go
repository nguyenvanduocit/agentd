package release

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// An app that passes the zero value of an argument gets a working default, not a panic in a goroutine of its own that
// would take the whole server down.
func TestWatchWithoutAnIntervalReadsOnceAndThenHourly(t *testing.T) {
	srv := releases(t, func() (int, []byte) { return http.StatusOK, real(t) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	l := Watch(ctx, srv.Client(), srv.URL, 0)
	waitVersion(t, l, "0.2.0")
}

func TestFetchWithoutAClientUsesTheDefaultOne(t *testing.T) {
	srv := releases(t, func() (int, []byte) { return http.StatusOK, real(t) })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	o, err := Fetch(ctx, nil, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if o.Version != "0.2.0" {
		t.Fatalf("version = %q, want 0.2.0", o.Version)
	}
}

func TestWatchWithoutAClientUsesTheDefaultOne(t *testing.T) {
	srv := releases(t, func() (int, []byte) { return http.StatusOK, real(t) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	l := Watch(ctx, nil, srv.URL, time.Hour)
	waitVersion(t, l, "0.2.0")
}
