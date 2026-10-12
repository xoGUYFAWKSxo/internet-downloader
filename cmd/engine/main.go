// Command engine is the background download engine. So far it owns the IPC
// pipe, keeps the streams the browser sends and checks their servers;
// downloads come in later phases.
//
// "engine -probe URL" checks one server and prints what it found, without
// starting the engine.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/version"
)

func main() {
	probeURL := flag.String("probe", "", "check this address's server, print the result and exit")
	flag.Parse()
	if *probeURL != "" {
		os.Exit(probeOnce(*probeURL))
	}
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
	if err := ipc.Serve(l, newEngine(os.Stderr).handle); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func probeOnce(u string) int {
	res, err := probe.Probe(context.Background(), probe.Request{URL: u}, probe.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "probe:", err)
		return 1
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
	return 0
}
