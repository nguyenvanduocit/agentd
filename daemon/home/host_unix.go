//go:build !windows

package home

import "syscall"

const xOK = 1 // access(2) X_OK

func canExecute(file string) bool {
	return syscall.Access(file, xOK) == nil
}
