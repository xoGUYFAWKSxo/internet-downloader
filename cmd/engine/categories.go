package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
)

// categories are the main window's file types, by extension. The window has
// the same list (TYPES in ui/frontend/src/list.ts); a test keeps them equal.
var categories = map[string]struct {
	label string
	ext   []string
}{
	"compressed": {"Compressed", []string{"zip", "rar", "7z", "tar", "gz", "tgz", "bz2", "xz", "zst", "iso", "cab"}},
	"documents":  {"Documents", []string{"pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "odt", "ods", "odp", "rtf", "txt", "csv", "epub", "md"}},
	"music":      {"Music", []string{"mp3", "m4a", "aac", "flac", "wav", "ogg", "opus", "wma"}},
	"programs":   {"Programs", []string{"exe", "msi", "msix", "appx", "appxbundle", "bat", "cmd", "ps1", "apk", "dmg", "deb", "rpm"}},
	"video":      {"Video", []string{"mp4", "mkv", "webm", "avi", "mov", "wmv", "flv", "m4v", "ts", "mpg", "mpeg", "3gp"}},
}

// categoryOf gives the category of a file name, or "".
func categoryOf(name string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if ext == "" {
		return ""
	}
	for c, v := range categories {
		for _, x := range v.ext {
			if x == ext {
				return c
			}
		}
	}
	return ""
}

// catDir is the folder Options gives name's category, or "".
func (e *engine) catDir(name string) string {
	c := categoryOf(name)
	if c == "" {
		return ""
	}
	return e.setting(setDirPrefix + c)
}

// dirFor is the folder to save name into when none was chosen: its
// category's, made if it isn't there, else the folder last used.
func (e *engine) dirFor(name string) string {
	if d := e.catDir(name); d != "" {
		err := os.MkdirAll(d, 0o755)
		if err == nil {
			return d
		}
		fmt.Fprintf(e.log, "couldn't use the %s folder, so saving into the download folder: %v\n", categories[categoryOf(name)].label, err)
	}
	return e.saveDir()
}

// limiter is j's own speed limit, under the one all downloads share.
func (e *engine) limiter(j *job) *fetch.Limiter {
	e.dl.mu.Lock()
	defer e.dl.mu.Unlock()
	if j.limiter == nil {
		j.limiter = e.limit.Child()
		j.limiter.SetRate(j.LimitKB << 10)
	}
	return j.limiter
}

// setJobLimit sets one download's speed limit in KB/s; 0 takes it off. It
// applies at once to a running download, and is kept for a paused one.
func (e *engine) setJobLimit(id uint64, kb int64) error {
	if kb < 0 || kb > 1000000 {
		return errors.New("The speed limit should be a whole number of KB/s, from 1 to 1000000, or empty for none.")
	}
	e.dl.mu.Lock()
	j := e.dl.findLocked(id)
	if j == nil {
		e.dl.mu.Unlock()
		return errors.New("That download is no longer in the list.")
	}
	j.LimitKB = kb
	if j.limiter != nil {
		j.limiter.SetRate(kb << 10)
	}
	e.dl.mu.Unlock()
	fmt.Fprintf(e.log, "download %d (%s): speed limit %d KB/s (0: none)\n", id, j.host, kb)
	e.save(j)
	return nil
}
