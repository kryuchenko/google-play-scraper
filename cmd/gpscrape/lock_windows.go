//go:build windows

package main

import "os"

// processAlive reports whether pid names a running process on this machine.
//
// Windows has no signals to probe with, so FindProcess is the check itself: it
// opens a handle to the process and fails when there is none. The handle is
// released immediately, because holding it keeps the exited process's record
// alive -- the opposite of the question being asked.
//
// It errs towards "alive": a process that has exited can still be openable
// while another handle to it is held. That direction is the safe one, since it
// costs a refusal a person can act on rather than a lock taken off a live run.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
