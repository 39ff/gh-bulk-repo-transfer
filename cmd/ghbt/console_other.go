//go:build !windows

package main

// startedByDoubleClick is only meaningful on Windows.
func startedByDoubleClick() bool { return false }
