package main

import (
	"fmt"
	"runtime"
	"time"

	"golang.org/x/sys/windows"
)

var setThreadExecutionState = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")

// stayAwake asks Windows not to go to sleep while on, as video players do.
// The screen can still turn off, and closing the lid or choosing Sleep still
// works. The request belongs to the calling thread.
func stayAwake(on bool) error {
	const continuous, systemRequired = 0x80000000, 0x00000001
	flags := uintptr(continuous)
	if on {
		flags |= systemRequired
	}
	if r, _, err := setThreadExecutionState.Call(flags); r == 0 {
		return err
	}
	return nil
}

// keepAwake keeps the computer awake while downloads run, checking every
// every, until quit closes. set is stayAwake outside tests.
func (e *engine) keepAwake(every time.Duration, set func(bool) error, quit <-chan struct{}) {
	runtime.LockOSThread() // the request is the thread's
	defer runtime.UnlockOSThread()
	on := false
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		want := e.setting(setAwake) == "1" && e.running() > 0
		if want != on {
			if err := set(want); err != nil {
				fmt.Fprintln(e.log, "engine: couldn't change whether the computer may sleep:", err)
			} else {
				on = want
			}
		}
		select {
		case <-quit:
			if on {
				set(false)
			}
			return
		case <-t.C:
		}
	}
}
