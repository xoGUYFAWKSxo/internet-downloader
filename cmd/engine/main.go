// Command engine is the background download engine. It owns the IPC pipe,
// keeps the streams the browser sends, checks their servers and downloads
// files and streams into the Downloads folder.
//
// "engine -probe URL" checks one server and prints what it found, and
// "engine -get URL" downloads one file or HLS/DASH stream, without starting
// the engine.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/journal"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/launch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/outfile"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/version"
)

func main() {
	probeURL := flag.String("probe", "", "check this address's server, print the result and exit")
	getURL := flag.String("get", "", "download this address, print progress and exit")
	dir := flag.String("dir", "", "with -get: the folder to save into (default: Downloads)")
	flag.Parse()
	if *probeURL != "" {
		os.Exit(probeOnce(*probeURL))
	}
	if *getURL != "" {
		os.Exit(getOnce(*getURL, *dir))
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
	e := newEngine(os.Stderr)
	e.show = showWindow
	if jr, err := journal.Open(journalPath()); err != nil {
		// Downloads still work; the list just isn't kept.
		fmt.Fprintln(os.Stderr, "engine: couldn't open the saved download list:", err)
	} else {
		defer jr.Close()
		if err := e.restore(jr); err != nil {
			fmt.Fprintln(os.Stderr, "engine: couldn't load the saved download list:", err)
		}
	}
	served := make(chan error, 1)
	go func() { served <- ipc.Serve(l, e.handle) }()
	runTray(e, l, served)
}

// journalPath is the saved download list, beside the engine's log.
func journalPath() string {
	dir := filepath.Dir(launch.LogPath())
	os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "downloads.db")
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

func getOnce(u, dir string) int {
	e := newEngine(os.Stderr)
	if dir != "" {
		e.dir = dir
	}
	stopped := make(chan uint64, 1)
	e.finished = func(id uint64) { stopped <- id }
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		<-sig
		e.cancelDownload(1)
	}()
	if kind := streamKind(u); kind != "" {
		e.downloadStream(media.Context{URL: u, Kind: kind})
	} else {
		e.download(probe.Request{URL: u})
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	last, at := int64(0), time.Now()
	for {
		select {
		case <-stopped:
			d := e.dl.list()[0]
			if d.State != fetch.Done {
				fmt.Fprintln(os.Stderr, "get:", d.Error)
				return 1
			}
			fmt.Println(d.Path)
			return 0
		case now := <-tick.C:
			d := e.dl.list()[0]
			if d.State == fetch.Joining {
				fmt.Fprintf(os.Stderr, "%s: joining the tracks with FFmpeg\n", d.Name)
			}
			if d.State != fetch.Downloading {
				continue
			}
			speed := float64(d.Done-last) / now.Sub(at).Seconds()
			last, at = d.Done, now
			of := "of " + outfile.Bytes(d.Size)
			if d.Parts > 0 {
				of = fmt.Sprintf("segment %d of %d", d.PartsDone, d.Parts)
			}
			fmt.Fprintf(os.Stderr, "%s: %s %s, %s/s (%s, %d streams)\n", d.Name,
				outfile.Bytes(d.Done), of, outfile.Bytes(int64(speed)), d.Protocol, d.Streams)
		}
	}
}

// streamKind tells HLS and DASH addresses from plain files by their
// extension; "" for anything else.
func streamKind(u string) string {
	pu, err := url.Parse(u)
	if err != nil {
		return ""
	}
	switch strings.ToLower(path.Ext(pu.Path)) {
	case ".m3u8", ".m3u":
		return media.KindHLS
	case ".mpd":
		return media.KindDASH
	}
	return ""
}
