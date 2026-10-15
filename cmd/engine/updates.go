package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// releasesPage is where the user gets a new version. It is fixed here, not
// taken from GitHub's answer.
const releasesPage = "https://github.com/xoGUYFAWKSxo/internet-downloader/releases/latest"

const latestRelease = "https://api.github.com/repos/xoGUYFAWKSxo/internet-downloader/releases/latest"

// updates is what the last check found.
type updates struct {
	mu     sync.Mutex
	newer  string // a newer version, or ""
	url    string // the address asked; tests point it at their own server
	client *http.Client
}

// newerVersion is the newer version found by the last check, or "".
func (e *engine) newerVersion() string {
	e.upd.mu.Lock()
	defer e.upd.mu.Unlock()
	return e.upd.newer
}

// checkUpdates asks GitHub for the latest release after first, then once
// every every. Nothing is downloaded or installed; the window and the tray
// offer the releases page. It runs until the engine stops.
func (e *engine) checkUpdates(first, every time.Duration) {
	t := time.NewTimer(first)
	for range t.C {
		e.checkUpdate(context.Background())
		t.Reset(every)
	}
}

func (e *engine) checkUpdate(ctx context.Context) {
	if e.setting(setUpdates) != "1" || parseVersion(e.version) == nil {
		e.setNewer("") // turned off, or a development build
		return
	}
	newer, err := e.latestVersion(ctx)
	if err != nil {
		fmt.Fprintln(e.log, "engine: couldn't check for a new version:", err)
		return
	}
	if newer != "" && newer != e.newerVersion() {
		fmt.Fprintf(e.log, "engine: version %s is available (this is %s)\n", newer, e.version)
	}
	e.setNewer(newer)
}

func (e *engine) setNewer(v string) {
	e.upd.mu.Lock()
	e.upd.newer = v
	e.upd.mu.Unlock()
}

// latestVersion is the latest release's version when it is newer than this
// one, else "".
func (e *engine) latestVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.upd.url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "GalacticDownloader/"+e.version)
	resp, err := e.upd.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var rel struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return "", fmt.Errorf("GitHub's answer wasn't readable: %w", err)
	}
	latest := strings.TrimPrefix(rel.Tag, "v")
	l := parseVersion(latest)
	if l == nil {
		return "", fmt.Errorf("GitHub's latest release has an unexpected name %q", rel.Tag)
	}
	if rel.Draft || rel.Prerelease || !newerThan(l, parseVersion(e.version)) {
		return "", nil
	}
	return latest, nil
}

// parseVersion reads "1.2.3"; nil for anything else, such as a
// development build's "0.0.0-dev".
func parseVersion(v string) []int {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nil
	}
	out := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil
		}
		out[i] = n
	}
	return out
}

func newerThan(a, b []int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}
