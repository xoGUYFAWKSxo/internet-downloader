package launch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
)

// The test binary stands in for engine.exe: run with LAUNCH_FAKE_PIPE set,
// it serves pings on that pipe for a few seconds.
func TestMain(m *testing.M) {
	if pipe := os.Getenv("LAUNCH_FAKE_PIPE"); pipe != "" {
		l, err := ipc.Listen(pipe)
		if err != nil {
			os.Exit(1)
		}
		fmt.Println("fake engine up")
		time.AfterFunc(10*time.Second, func() { l.Close() })
		ipc.Serve(l, func(m ipc.Message) ipc.Message {
			if m.Type == "quit" {
				time.AfterFunc(50*time.Millisecond, func() { os.Exit(0) })
			}
			return ipc.Message{Type: "pong"}
		})
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestEnsureStartsEngine(t *testing.T) {
	pipe := fmt.Sprintf(`\\.\pipe\InternetDownloader-launch-test-%d`, os.Getpid())
	t.Setenv("LAUNCH_FAKE_PIPE", pipe)
	self, _ := os.Executable()
	log := filepath.Join(t.TempDir(), "logs", "engine.log")
	ctx := context.Background()
	if running(ctx, pipe) {
		t.Fatal("nothing should be on the pipe yet")
	}
	if err := EnsureWith(ctx, pipe, self, log); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopFake(t, pipe, log) })
	if !running(ctx, pipe) {
		t.Fatal("engine not answering")
	}
	// Already running: nothing new is started.
	if err := EnsureWith(ctx, pipe, filepath.Join(t.TempDir(), "missing.exe"), log); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "fake engine up") {
		t.Fatalf("log = %q", b)
	}
}

// stopFake ends the fake engine and waits until its log can be deleted.
func stopFake(t *testing.T, pipe, log string) {
	if c, err := ipc.Dial(context.Background(), pipe); err == nil {
		c.Call(ipc.Message{Type: "quit"})
		c.Close()
	}
	for i := 0; i < 50; i++ {
		if os.Remove(log) == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("the fake engine didn't exit")
}

func TestEnsureMissingEngine(t *testing.T) {
	pipe := fmt.Sprintf(`\\.\pipe\InternetDownloader-launch-missing-%d`, os.Getpid())
	err := EnsureWith(context.Background(), pipe, filepath.Join(t.TempDir(), "engine.exe"), filepath.Join(t.TempDir(), "l.log"))
	if err == nil || !strings.Contains(err.Error(), "couldn't find the download engine") {
		t.Fatalf("err = %v", err)
	}
}

// The portable version keeps its data beside the programs.
func TestDataDir(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "engine.exe")
	if got := dataDir(self, `C:\Users\x\AppData\Local`); got != `C:\Users\x\AppData\Local\InternetDownloader` {
		t.Fatalf("installed: %s", got)
	}
	if err := os.WriteFile(filepath.Join(dir, PortableMark), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := dataDir(self, `C:\Users\x\AppData\Local`); got != filepath.Join(dir, "data") {
		t.Fatalf("portable: %s", got)
	}
	// A folder of that name isn't the mark.
	other := t.TempDir()
	os.Mkdir(filepath.Join(other, PortableMark), 0o755)
	if portable(filepath.Join(other, "ui.exe")) {
		t.Fatal("a folder counted as the mark")
	}
}
