//go:build unix

package main

import (
	"errors"
	"os"
	"syscall"
)

// processAlive reports whether pid names a running process on this machine.
//
// Signal 0 is the POSIX probe: the kernel runs the existence and permission
// checks and delivers nothing. EPERM is an answer rather than a failure -- a
// process owned by another user is still a process, and reading that as "gone"
// would let one user's sweep take the lock off another's. os.FindProcess never
// fails here (it looks nothing up), so the probe is where the answer comes
// from.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
