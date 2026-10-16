package main

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// When the queue finishes by itself, the computer can be put to sleep,
// hibernated, turned off or restarted, as IDM's queue can. It is never done at once: a window counts
// down for a minute, and closing it or Cancel stops it.
const (
	powerSleep     = "sleep"
	powerHibernate = "hibernate"
	powerOff       = "off"
	powerRestart   = "restart"
)

type powerState struct {
	mu     sync.Mutex
	action string // "" when no countdown runs
	at     time.Time
	timer  *time.Timer
}

// PowerInfo is what the countdown window shows: the action, and the seconds
// left. Action is "" when nothing is going to happen.
type PowerInfo struct {
	Action string `json:"action,omitempty"`
	Left   int    `json:"left,omitempty"`
}

// queueFinished is called when the queue stopped by itself after starting
// something; when Options says so, it starts the countdown.
func (e *engine) queueFinished() {
	action := e.setting(setWhenDone)
	if action == "" {
		return
	}
	if e.power == nil || e.show == nil {
		fmt.Fprintf(e.log, "queue: can't %s here\n", powerText(action))
		return
	}
	e.pw.mu.Lock()
	if e.pw.action != "" {
		e.pw.mu.Unlock()
		return
	}
	e.pw.action, e.pw.at = action, e.now().Add(e.powerDelay)
	e.pw.timer = time.AfterFunc(e.powerDelay, func() { e.powerNow("the countdown") })
	e.pw.mu.Unlock()
	fmt.Fprintf(e.log, "queue: finished; going to %s in %s unless cancelled\n", powerText(action), e.powerDelay)
	// Without the window nobody could cancel it, so there's no countdown.
	if err := e.show("--power"); err != nil {
		e.cancelPower("")
		fmt.Fprintf(e.log, "queue: couldn't show the countdown, so not going to %s: %v\n", powerText(action), err)
	}
}

func (e *engine) powerInfo() PowerInfo {
	e.pw.mu.Lock()
	defer e.pw.mu.Unlock()
	if e.pw.action == "" {
		return PowerInfo{}
	}
	left := int((e.pw.at.Sub(e.now()) + time.Second - 1) / time.Second)
	return PowerInfo{Action: e.pw.action, Left: max(0, left)}
}

// cancelPower stops the countdown; by, if not "", is logged.
func (e *engine) cancelPower(by string) {
	e.pw.mu.Lock()
	action := e.pw.action
	if e.pw.timer != nil {
		e.pw.timer.Stop()
	}
	e.pw.action, e.pw.timer = "", nil
	e.pw.mu.Unlock()
	if action != "" && by != "" {
		fmt.Fprintf(e.log, "queue: not going to %s: cancelled by %s\n", powerText(action), by)
	}
}

// powerNow does what the countdown was for, unless it was cancelled or a
// download has started since.
func (e *engine) powerNow(by string) error {
	e.pw.mu.Lock()
	action := e.pw.action
	if e.pw.timer != nil {
		e.pw.timer.Stop()
	}
	e.pw.action, e.pw.timer = "", nil
	e.pw.mu.Unlock()
	if action == "" {
		return errors.New("Nothing is going to happen now.")
	}
	if n := e.running(); n > 0 {
		fmt.Fprintf(e.log, "queue: not going to %s: a download has started\n", powerText(action))
		return errors.New("A download has started, so the computer stays on.")
	}
	fmt.Fprintf(e.log, "queue: going to %s (%s)\n", powerText(action), by)
	if err := e.power(action); err != nil {
		fmt.Fprintf(e.log, "queue: couldn't %s: %v\n", powerText(action), err)
		return fmt.Errorf("Couldn't %s: %v", powerText(action), err)
	}
	return nil
}

func powerText(action string) string {
	switch action {
	case powerSleep:
		return "put the computer to sleep"
	case powerHibernate:
		return "hibernate the computer"
	case powerRestart:
		return "restart the computer"
	}
	return "turn the computer off"
}

var (
	powrprof              = windows.NewLazySystemDLL("powrprof.dll")
	setSuspendState       = powrprof.NewProc("SetSuspendState")
	isPwrHibernateAllowed = powrprof.NewProc("IsPwrHibernateAllowed")
)

// errNoHibernate is for a computer with hibernation turned off, as Windows
// lets an administrator do.
var errNoHibernate = errors.New("Hibernation is turned off on this computer. Choose another action, or turn it on (as an administrator: powercfg /hibernate on).")

// canHibernate tells whether Windows would hibernate now.
func canHibernate() bool {
	r, _, _ := isPwrHibernateAllowed.Call()
	return r != 0
}

// powerDo puts the computer to sleep, hibernates it, turns it off or
// restarts it, as the Start menu does. Turning off and restarting aren't
// forced: a program with unsaved work can still ask to keep it on.
func powerDo(action string) error {
	switch action {
	case powerSleep, powerHibernate:
		hibernate := uintptr(0)
		if action == powerHibernate {
			if !canHibernate() {
				return errNoHibernate
			}
			hibernate = 1
		}
		if err := allowShutdown(); err != nil {
			return err
		}
		if r, _, err := setSuspendState.Call(hibernate, 0, 0); r == 0 {
			return err
		}
		return nil
	case powerOff, powerRestart:
		dir, err := windows.GetSystemDirectory()
		if err != nil {
			return err
		}
		how := "/s"
		if action == powerRestart {
			how = "/r"
		}
		cmd := exec.Command(filepath.Join(dir, "shutdown.exe"), how, "/t", "0")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%v %s", err, out)
		}
		return nil
	}
	return fmt.Errorf("unknown action %q", action)
}

// allowShutdown turns on the shut-down right every user has, which Windows
// keeps off until a program asks for it. It changes no setting.
func allowShutdown() error {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		return err
	}
	defer tok.Close()
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr("SeShutdownPrivilege"), &luid); err != nil {
		return err
	}
	p := windows.Tokenprivileges{PrivilegeCount: 1}
	p.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
	return windows.AdjustTokenPrivileges(tok, false, &p, uint32(unsafe.Sizeof(p)), nil, nil)
}
