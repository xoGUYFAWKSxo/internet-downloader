// Package nativemsg implements Chrome/Edge Native Messaging framing:
// each message is a 32-bit length in native byte order (little-endian on
// Windows x64) followed by that many bytes of UTF-8 JSON.
package nativemsg

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// MaxFromBrowser is the browser's limit for messages sent to the host.
	MaxFromBrowser = 64 << 20
	// MaxToBrowser is the browser's limit for messages sent by the host.
	MaxToBrowser = 1 << 20
)

var ErrTooLarge = errors.New("nativemsg: message exceeds size limit")

// Read reads one message. It returns io.EOF when the browser closes stdin
// cleanly between messages.
func Read(r io.Reader) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return nil, err
	}
	if n > MaxFromBrowser {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("nativemsg: truncated message: %w", err)
	}
	if !json.Valid(buf) {
		return nil, errors.New("nativemsg: message is not valid JSON")
	}
	return buf, nil
}

// Write writes one message. The length prefix and body go out in a single
// Write so a reader never sees a prefix without its body.
func Write(w io.Writer, msg []byte) error {
	if len(msg) > MaxToBrowser {
		return fmt.Errorf("%w: %d bytes", ErrTooLarge, len(msg))
	}
	buf := make([]byte, 4+len(msg))
	binary.LittleEndian.PutUint32(buf, uint32(len(msg)))
	copy(buf[4:], msg)
	_, err := w.Write(buf)
	return err
}
