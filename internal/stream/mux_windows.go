package stream

import (
	"os/exec"
	"syscall"
)

// hide keeps FFmpeg from opening a console window.
func hide(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
