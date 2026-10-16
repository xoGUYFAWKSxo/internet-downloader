// Package launch starts the engine when nothing answers on the pipe, so the
// window and the browser relay can each bring it up and nobody has to start
// it by hand.
package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
)

// maxLog is the size past which the engine's log starts over.
const maxLog = 1 << 20

// Ensure returns once the engine answers on the pipe. If it isn't running,
// it starts engine.exe from the folder this program is in, with its output
// going to a log file, and waits for it.
func Ensure(ctx context.Context, pipe string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	return EnsureWith(ctx, pipe, filepath.Join(filepath.Dir(self), "engine.exe"), LogPath())
}

// EnsureWith is Ensure with the engine and log paths given.
func EnsureWith(ctx context.Context, pipe, engine, log string) error {
	if running(ctx, pipe) {
		return nil
	}
	if _, err := os.Stat(engine); err != nil {
		return fmt.Errorf("couldn't find the download engine (%s)", engine)
	}
	if err := start(engine, log); err != nil {
		return fmt.Errorf("couldn't start the download engine: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		if running(ctx, pipe) {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("the download engine was started but didn't answer; see " + log)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// LogPath is where a started engine writes its log.
func LogPath() string {
	return filepath.Join(DataDir(), "engine.log")
}

// PortableMark is the file that, next to the programs, makes them keep
// everything in a "data" folder beside them: the portable zip has one.
const PortableMark = "portable.txt"

// DataDir is the folder for the log, the download list and the window's
// own files: %LOCALAPPDATA%\InternetDownloader, or "data" beside the
// programs in the portable version.
func DataDir() string {
	self, err := os.Executable()
	if err != nil {
		self = ""
	}
	return dataDir(self, os.Getenv("LOCALAPPDATA"))
}

// Portable tells whether this program is the portable version.
func Portable() bool {
	self, err := os.Executable()
	return err == nil && portable(self)
}

func portable(self string) bool {
	if self == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(filepath.Dir(self), PortableMark))
	return err == nil && fi.Mode().IsRegular()
}

func dataDir(self, localAppData string) string {
	if portable(self) {
		return filepath.Join(filepath.Dir(self), "data")
	}
	return filepath.Join(localAppData, "InternetDownloader")
}

func running(ctx context.Context, pipe string) bool {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	c, err := ipc.Dial(ctx, pipe)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func start(engine, log string) error {
	if err := os.MkdirAll(filepath.Dir(log), 0o700); err != nil {
		return err
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if fi, err := os.Stat(log); err == nil && fi.Size() > maxLog {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	f, err := os.OpenFile(log, flags, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	const (
		newProcessGroup  = 0x00000200
		breakawayFromJob = 0x01000000
		createNoWindow   = 0x08000000
	)
	run := func(flags uint32) error {
		cmd := exec.Command(engine)
		cmd.Dir = filepath.Dir(engine)
		cmd.Stdout, cmd.Stderr = f, f
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	// Browsers put the relay in a job that is closed with it; breaking away
	// keeps the engine (and its downloads) alive after the browser exits.
	// Some jobs forbid that, and then the engine stays in the job.
	if err := run(createNoWindow | newProcessGroup | breakawayFromJob); err == nil {
		return nil
	}
	return run(createNoWindow | newProcessGroup)
}
