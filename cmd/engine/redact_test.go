package main

import (
	"bytes"
	"fmt"
	"testing"
)

func TestRedactedLog(t *testing.T) {
	var b bytes.Buffer
	w := redacted{&b}
	for in, want := range map[string]string{
		`download 1 (a.test): failed: Get "https://a.test/v/seg1.ts?token=SECRET": dial tcp: refused` + "\n": `download 1 (a.test): failed: Get "https://a.test/…": dial tcp: refused` + "\n",
		"see http://user:pw@b.test:8080/x/SECRET/y and https://c.test/\n":                                    "see http://b.test:8080/… and https://c.test\n",
		"engine: no address here\n": "engine: no address here\n",
	} {
		b.Reset()
		if n, err := fmt.Fprint(w, in); err != nil || n != len(in) {
			t.Fatalf("wrote %d, %v", n, err)
		}
		if b.String() != want {
			t.Errorf("%q\ngot  %q\nwant %q", in, b.String(), want)
		}
	}
}
