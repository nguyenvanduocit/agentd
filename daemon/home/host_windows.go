//go:build windows

package home

// Windows has no execute bit: a file that is found by one of PATHEXT is executable.
func canExecute(string) bool { return true }
