package outfile

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Phase 3 comparison (see DECISIONS.md): writers each fill their own
// slice of one file in network-sized chunks, as a split download does.
//
//	go test ./internal/outfile -run - -bench . -benchtime 3x
const (
	benchSize  = 1 << 30
	benchChunk = 256 << 10
)

func benchLayouts(b *testing.B, writers int) {
	chunk := make([]byte, benchChunk)
	for i := range chunk {
		chunk[i] = byte(i)
	}
	run := func(b *testing.B, open func(path string) (write func([]byte, int64), done func())) {
		b.SetBytes(benchSize)
		for range b.N {
			path := filepath.Join(b.TempDir(), "f")
			write, done := open(path)
			var wg sync.WaitGroup
			part := int64(benchSize / writers)
			for w := range writers {
				wg.Go(func() {
					for off := int64(w) * part; off < int64(w+1)*part; off += benchChunk {
						write(chunk, off)
					}
				})
			}
			wg.Wait()
			done()
		}
	}
	b.Run("reserve+WriteAt", func(b *testing.B) {
		run(b, func(path string) (func([]byte, int64), func()) {
			o, err := Create(filepath.Dir(path), "f", benchSize)
			if err != nil {
				b.Fatal(err)
			}
			return func(p []byte, off int64) {
					if _, err := o.WriteAt(p, off); err != nil {
						b.Fatal(err)
					}
				}, func() {
					if _, err := o.Finish(); err != nil {
						b.Fatal(err)
					}
				}
		})
	})
	b.Run("sparse+WriteAt", func(b *testing.B) {
		run(b, func(path string) (func([]byte, int64), func()) {
			f, err := os.Create(path)
			if err != nil {
				b.Fatal(err)
			}
			var n uint32
			if err := windows.DeviceIoControl(windows.Handle(f.Fd()), windows.FSCTL_SET_SPARSE, nil, 0, nil, 0, &n, nil); err != nil {
				b.Fatal(err)
			}
			f.Truncate(benchSize)
			return func(p []byte, off int64) {
					if _, err := f.WriteAt(p, off); err != nil {
						b.Fatal(err)
					}
				}, func() {
					f.Sync()
					f.Close()
				}
		})
	})
	b.Run("reserve+mmap", func(b *testing.B) {
		run(b, func(path string) (func([]byte, int64), func()) {
			o, err := Create(filepath.Dir(path), "f", benchSize)
			if err != nil {
				b.Fatal(err)
			}
			h, err := windows.CreateFileMapping(windows.Handle(o.f.Fd()), nil, windows.PAGE_READWRITE, 0, 0, nil)
			if err != nil {
				b.Fatal(err)
			}
			addr, err := windows.MapViewOfFile(h, windows.FILE_MAP_WRITE, 0, 0, benchSize)
			if err != nil {
				b.Fatal(err)
			}
			view := unsafe.Slice((*byte)(*(*unsafe.Pointer)(unsafe.Pointer(&addr))), benchSize) // vet-safe uintptr to pointer
			return func(p []byte, off int64) { copy(view[off:], p) }, func() {
				windows.FlushViewOfFile(addr, benchSize)
				windows.UnmapViewOfFile(addr)
				windows.CloseHandle(h)
				o.f.Sync()
				o.Abort()
			}
		})
	})
}

func BenchmarkWriters4(b *testing.B) { benchLayouts(b, 4) }
func BenchmarkWriters8(b *testing.B) { benchLayouts(b, 8) }
