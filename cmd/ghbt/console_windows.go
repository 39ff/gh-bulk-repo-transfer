//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// startedByDoubleClick reports whether this process is the only one attached
// to its console, which means Windows opened the console window just for it
// (e.g. ghbt.exe was double-clicked) and will close it as soon as we exit.
func startedByDoubleClick() bool {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleProcessList")
	if proc.Find() != nil {
		return false
	}
	var pids [2]uint32
	n, _, _ := proc.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1
}
