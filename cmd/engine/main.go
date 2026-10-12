// Command engine is the background download engine. Phase 0: it owns the
// IPC pipe and answers ping; downloads come in later phases.
package main

import (
	"fmt"
	"os"
	"os/signal"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/version"
)

func main() {
	name, err := ipc.PipeName()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	l, err := ipc.Listen(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "engine: could not open the pipe (is another engine already running?):", err)
		os.Exit(1)
	}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		<-sig
		l.Close()
	}()
	fmt.Fprintln(os.Stderr, "engine", version.Version, "listening on", name)
	if err := ipc.Serve(l, handle); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
