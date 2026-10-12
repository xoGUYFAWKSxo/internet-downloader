package outfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// reserve claims size bytes of disk for f, then sets the end of the file.
// Claiming first makes a full disk fail now, with a clear message, rather
// than halfway through the download. It needs no admin rights, unlike
// SetFileValidData, and never exposes old disk contents: Windows reads
// unwritten space back as zeros.
func reserve(f *os.File, size int64) error {
	var free uint64
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(f.Name()))
	if windows.GetDiskFreeSpaceEx(dir, &free, nil, nil) == nil && uint64(size) > free {
		return noSpace(size, int64(free))
	}
	info := struct{ AllocationSize int64 }{size}
	err := windows.SetFileInformationByHandle(windows.Handle(f.Fd()), windows.FileAllocationInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if errors.Is(err, windows.ERROR_DISK_FULL) {
		return noSpace(size, -1)
	}
	if err != nil {
		return fmt.Errorf("couldn't reserve space for the file: %w", err)
	}
	return f.Truncate(size)
}

func noSpace(size, free int64) error {
	msg := fmt.Sprintf("there isn't enough free space on the disk for this %s file", Bytes(size))
	if free >= 0 {
		msg += fmt.Sprintf(" (%s free)", Bytes(free))
	}
	return errors.New(msg)
}

// Bytes formats n for messages, e.g. "1.5 GB".
func Bytes(n int64) string {
	units := []string{"bytes", "KB", "MB", "GB", "TB"}
	v, i := float64(n), 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d bytes", n)
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
