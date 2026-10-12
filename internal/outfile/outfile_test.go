package outfile

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestPartsLandAtTheirOffsets(t *testing.T) {
	dir := t.TempDir()
	want := bytes.Repeat([]byte("0123456789abcdef"), 4096) // 64 KiB
	o, err := Create(dir, "movie.mp4", int64(len(want)))
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "movie.mp4.part")); err != nil || fi.Size() != int64(len(want)) {
		t.Fatalf("part file: %v %v", fi, err)
	}
	// Eight writers, each filling its slice backwards in 1 KiB pieces.
	var wg sync.WaitGroup
	part := len(want) / 8
	for w := range 8 {
		wg.Go(func() {
			for off := (w+1)*part - 1024; off >= w*part; off -= 1024 {
				if _, err := o.WriteAt(want[off:off+1024], int64(off)); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if o.Written() != int64(len(want)) || len(o.Missing()) != 0 {
		t.Fatalf("written %d, missing %v", o.Written(), o.Missing())
	}
	path, err := o.Finish()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if filepath.Base(path) != "movie.mp4" || !bytes.Equal(got, want) {
		t.Fatalf("%s: content differs", path)
	}
	if _, err := os.Stat(filepath.Join(dir, "movie.mp4.part")); !os.IsNotExist(err) {
		t.Fatal(".part left behind")
	}
}

func TestIncompleteFileIsNotFinished(t *testing.T) {
	dir := t.TempDir()
	o, _ := Create(dir, "a.bin", 100)
	o.WriteAt(make([]byte, 10), 0)
	o.WriteAt(make([]byte, 10), 50)
	o.WriteAt(make([]byte, 10), 5) // overlaps: counted once
	if o.Written() != 25 {
		t.Fatalf("written %d", o.Written())
	}
	if want := []Range{{15, 50}, {60, 100}}; !reflect.DeepEqual(o.Missing(), want) {
		t.Fatalf("missing %v", o.Missing())
	}
	if _, err := o.Finish(); err == nil || !strings.Contains(err.Error(), "75 bytes missing") {
		t.Fatalf("finish: %v", err)
	}
	if _, err := o.WriteAt(make([]byte, 2), 99); err == nil {
		t.Fatal("write past the end accepted")
	}
	if err := o.Abort(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("left %v", entries)
	}
}

func TestNamesDoNotClash(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "clip.mp4"), nil, 0o644)
	a, _ := Create(dir, "clip.mp4", 0)
	b, _ := Create(dir, "clip.mp4", 0)
	if a.Name() != "clip (1).mp4" || b.Name() != "clip (2).mp4" {
		t.Fatalf("names %q %q", a.Name(), b.Name())
	}
	// Someone takes a's name before it finishes.
	os.WriteFile(filepath.Join(dir, "clip (1).mp4"), []byte("theirs"), 0o644)
	defer b.Abort()
	path, err := a.Finish()
	if err != nil || filepath.Base(path) != "clip (3).mp4" {
		t.Fatalf("finish %q %v", path, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "clip (1).mp4")); string(got) != "theirs" {
		t.Fatal("overwrote another file")
	}
}

func TestUnknownSizeGrows(t *testing.T) {
	o, _ := Create(t.TempDir(), "s", -1)
	o.WriteAt([]byte("hello"), 0)
	o.WriteAt([]byte(" world"), 5)
	path, err := o.Finish()
	if got, _ := os.ReadFile(path); err != nil || string(got) != "hello world" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestNoSpace(t *testing.T) {
	_, err := Create(t.TempDir(), "huge", 1<<50)
	if err == nil || !strings.Contains(err.Error(), "free space") {
		t.Fatalf("got %v", err)
	}
}

func TestCleanName(t *testing.T) {
	cases := map[string]string{
		"movie.mp4":                       "movie.mp4",
		`..\..\evil.exe`:                  "evil.exe",
		"a/b/c.mkv":                       "c.mkv",
		`what?: "x" <y>|*.mp4`:            "what__ _x_ _y___.mp4",
		"CON.txt":                         "_CON.txt",
		"com3":                            "_com3",
		"COMPUTER.txt":                    "COMPUTER.txt",
		"trailing. . ":                    "trailing",
		"":                                "download",
		"tab\there":                       "tab_here",
		strings.Repeat("x", 300) + ".mp4": strings.Repeat("x", 196) + ".mp4",
	}
	for in, want := range cases {
		if got := CleanName(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestMerge(t *testing.T) {
	var rs []Range
	for _, r := range []Range{{10, 20}, {30, 40}, {0, 5}, {20, 30}, {4, 11}, {50, 60}} {
		rs = merge(rs, r)
	}
	if want := []Range{{0, 40}, {50, 60}}; !reflect.DeepEqual(rs, want) {
		t.Fatalf("got %v", rs)
	}
}

func TestBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 bytes", 1023: "1023 bytes", 1536: "1.5 KB", 3 << 30: "3.0 GB"} {
		if got := Bytes(n); got != want {
			t.Errorf("%d: %q", n, got)
		}
	}
}

func TestReplace(t *testing.T) {
	dir := t.TempDir()
	o, err := Create(dir, "clip.mp4", -1)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "made-elsewhere")
	os.WriteFile(src, []byte("joined"), 0o644)
	path, err := o.Replace(src)
	b, _ := os.ReadFile(path)
	if err != nil || filepath.Base(path) != "clip.mp4" || string(b) != "joined" || o.Written() != 6 {
		t.Fatalf("%v %s %q", err, path, b)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source left behind")
	}
}

func TestReopen(t *testing.T) {
	dir := t.TempDir()
	o, err := Create(dir, "film.mkv", 100)
	if err != nil {
		t.Fatal(err)
	}
	o.WriteAt(bytes.Repeat([]byte("a"), 40), 0)
	o.WriteAt(bytes.Repeat([]byte("c"), 20), 80)
	have := o.Have()
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(o.Path(), 99, have); err == nil {
		t.Fatal("opened with the wrong size")
	}
	r, err := Open(o.Path(), 100, have)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name() != "film.mkv" || r.Written() != 60 || !reflect.DeepEqual(r.Missing(), []Range{{40, 80}}) {
		t.Fatalf("name %q, written %d, missing %v", r.Name(), r.Written(), r.Missing())
	}
	r.WriteAt(bytes.Repeat([]byte("b"), 40), 40)
	path, err := r.Finish()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != strings.Repeat("a", 40)+strings.Repeat("b", 40)+strings.Repeat("c", 20) {
		t.Fatalf("file = %q", b)
	}
}
