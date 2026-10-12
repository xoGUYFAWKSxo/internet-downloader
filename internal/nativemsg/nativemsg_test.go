package nativemsg

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	msgs := []string{`{"a":1}`, `"x"`, `[]`}
	for _, m := range msgs {
		if err := Write(&buf, []byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range msgs {
		got, err := Read(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	if _, err := Read(&buf); err != io.EOF {
		t.Fatalf("want io.EOF at end, got %v", err)
	}
}

func TestReadRejectsOversize(t *testing.T) {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint32(MaxFromBrowser+1))
	if _, err := Read(&buf); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestReadRejectsTruncated(t *testing.T) {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint32(10))
	buf.WriteString(`{"a"`)
	if _, err := Read(&buf); err == nil || err == io.EOF {
		t.Fatalf("want truncation error, got %v", err)
	}
}

func TestReadRejectsInvalidJSON(t *testing.T) {
	var buf bytes.Buffer
	Write(&buf, []byte(`{not json`))
	if _, err := Read(&buf); err == nil {
		t.Fatal("want error for invalid JSON")
	}
}

func TestWriteRejectsOversize(t *testing.T) {
	if err := Write(io.Discard, make([]byte, MaxToBrowser+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}
