// Package outfile is the one file a download is written into. When the size
// is known, the whole file is reserved on disk up front, and each part is
// written at its own offset as it arrives, from as many goroutines as there
// are streams.
//
// The file is written as "<name>.part" and renamed to its real name only when
// every byte is in, so a half-written file never looks finished.
package outfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

const partSuffix = ".part"

// Range is the half-open byte span [Start, End).
type Range struct {
	Start, End int64
}

type File struct {
	f    *os.File
	dir  string
	base string // the cleaned name asked for
	name string // final name, without the suffix
	size int64  // -1 when unknown

	written atomic.Int64

	mu    sync.Mutex
	have  []Range // sorted, merged spans already written
	final string  // set by Finish
}

// Create makes "<dir>/<name>.part" and reserves size bytes for it. The name
// is cleaned for Windows, and " (1)", " (2)"... is added if a file of that
// name (or its .part) is already there. size -1 means unknown: the file then
// grows as it is written.
func Create(dir, base string, size int64) (*File, error) {
	name := CleanName(base)
	var f *os.File
	for i := 0; ; i++ {
		n := numbered(name, i)
		if exists(filepath.Join(dir, n)) {
			continue
		}
		var err error
		f, err = os.OpenFile(filepath.Join(dir, n+partSuffix), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		name = n
		break
	}
	o := &File{f: f, dir: dir, base: CleanName(base), name: name, size: size}
	if size > 0 {
		if err := reserve(f, size); err != nil {
			f.Close()
			os.Remove(f.Name())
			return nil, err
		}
	}
	return o, nil
}

// Open reopens a .part file left by an earlier run to finish it. have lists
// the spans known to be on disk; the rest is fetched again.
func Open(part string, size int64, have []Range) (*File, error) {
	if !strings.HasSuffix(part, partSuffix) || size < 0 {
		return nil, fmt.Errorf("%s isn't an unfinished download", filepath.Base(part))
	}
	f, err := os.OpenFile(part, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || fi.Size() != size {
		f.Close()
		return nil, fmt.Errorf("%s isn't the size it was", filepath.Base(part))
	}
	name := strings.TrimSuffix(filepath.Base(part), partSuffix)
	o := &File{f: f, dir: filepath.Dir(part), base: name, name: name, size: size}
	for _, r := range have {
		if r.Start >= 0 && r.End <= size && r.Start < r.End {
			o.mark(r)
		}
	}
	return o, nil
}

// Path is where the unfinished file is (the .part file).
func (o *File) Path() string { return o.f.Name() }

// Have lists the spans written so far, sorted and merged.
func (o *File) Have() []Range {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Range(nil), o.have...)
}

// Sync flushes what has been written to the disk.
func (o *File) Sync() error { return o.f.Sync() }

// Close closes the file and keeps the .part, so it can be finished later.
func (o *File) Close() error { return o.f.Close() }

// Name is the file's final name (without .part).
func (o *File) Name() string { return o.name }

// Size is the file's size, or -1 when unknown.
func (o *File) Size() int64 { return o.size }

// Written counts the bytes written so far.
func (o *File) Written() int64 { return o.written.Load() }

// WriteAt writes p at off. It is safe to call from several goroutines for
// different spans of the file.
func (o *File) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || (o.size >= 0 && off+int64(len(p)) > o.size) {
		return 0, fmt.Errorf("a write at %d-%d falls outside the file's %d bytes", off, off+int64(len(p)), o.size)
	}
	n, err := o.f.WriteAt(p, off)
	if n > 0 {
		o.mark(Range{off, off + int64(n)})
	}
	return n, err
}

func (o *File) mark(r Range) {
	o.mu.Lock()
	defer o.mu.Unlock()
	before := covered(o.have)
	o.have = merge(o.have, r)
	o.written.Add(covered(o.have) - before)
}

// Missing lists the spans not written yet. With an unknown size it is empty.
func (o *File) Missing() []Range {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.size < 0 {
		return nil
	}
	var out []Range
	at := int64(0)
	for _, r := range o.have {
		if r.Start > at {
			out = append(out, Range{at, r.Start})
		}
		at = r.End
	}
	if at < o.size {
		out = append(out, Range{at, o.size})
	}
	return out
}

// Finish flushes the file to disk and gives it its real name. It fails, and
// leaves the .part file, if any byte is missing.
func (o *File) Finish() (string, error) {
	if m := o.Missing(); len(m) > 0 {
		return "", fmt.Errorf("the file isn't complete: %d bytes missing", o.size-o.Written())
	}
	if err := o.f.Sync(); err != nil {
		return "", err
	}
	if err := o.f.Close(); err != nil {
		return "", err
	}
	return o.rename()
}

// Replace finishes the file with src, a complete file made elsewhere on the
// same disk (such as FFmpeg's output), and names it as Finish does.
func (o *File) Replace(src string) (string, error) {
	if err := o.f.Close(); err != nil {
		return "", err
	}
	fi, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if err := os.Rename(src, o.f.Name()); err != nil {
		return "", err
	}
	o.mu.Lock()
	o.size, o.have = fi.Size(), []Range{{0, fi.Size()}}
	o.mu.Unlock()
	o.written.Store(fi.Size())
	return o.rename()
}

func (o *File) rename() (string, error) {
	part := o.f.Name()
	// os.Rename replaces an existing file on Windows; a file of the same
	// name may have appeared since Create, so look again.
	for i := 0; ; i++ {
		n := o.name
		if i > 0 {
			n = numbered(o.base, i)
		}
		dst := filepath.Join(o.dir, n)
		// Another download may be writing dst.part; leave that name to it.
		if exists(dst) || (i > 0 && exists(dst+partSuffix)) {
			continue
		}
		if err := os.Rename(part, dst); err != nil {
			return "", err
		}
		o.name, o.final = n, dst
		return dst, nil
	}
}

// Abort closes and deletes the .part file.
func (o *File) Abort() error {
	o.f.Close()
	return os.Remove(o.f.Name())
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// numbered gives "name.ext", then "name (1).ext", "name (2).ext"...
func numbered(name string, i int) string {
	if i == 0 {
		return name
	}
	ext := filepath.Ext(name)
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext)
}

var reserved = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}

// CleanName makes a server-supplied name safe as a Windows file name: no
// folders, no characters Windows refuses, no device names, and not too long.
func CleanName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	var b strings.Builder
	for _, r := range name {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			r = '_'
		}
		b.WriteRune(r)
	}
	name = strings.TrimRight(strings.TrimSpace(b.String()), ". ")
	stem := strings.ToUpper(strings.TrimSuffix(name, filepath.Ext(name)))
	if reserved[stem] || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '0' && stem[3] <= '9') {
		name = "_" + name
	}
	if name == "" || name == "_" {
		name = "download"
	}
	// Leave room for " (99)" and ".part" under the usual 255-character limit.
	if r := []rune(name); len(r) > 200 {
		ext := []rune(filepath.Ext(name))
		if len(ext) > 20 {
			ext = nil
		}
		name = string(r[:200-len(ext)]) + string(ext)
	}
	return name
}

// merge adds r to the sorted, merged list rs.
func merge(rs []Range, r Range) []Range {
	i := sort.Search(len(rs), func(i int) bool { return rs[i].End >= r.Start })
	j := i
	for j < len(rs) && rs[j].Start <= r.End {
		r.Start = min(r.Start, rs[j].Start)
		r.End = max(r.End, rs[j].End)
		j++
	}
	out := append(rs[:i:i], r)
	return append(out, rs[j:]...)
}

func covered(rs []Range) int64 {
	var n int64
	for _, r := range rs {
		n += r.End - r.Start
	}
	return n
}
