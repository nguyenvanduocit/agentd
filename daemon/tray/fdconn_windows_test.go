package tray

import "testing"

// The app's channel is a Unix socket pair; there is none to test on Windows.
func testFDConn(t *testing.T) { t.Skip("no inherited socket pair on Windows") }
