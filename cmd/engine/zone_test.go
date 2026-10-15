package main

import (
	"os"
	"strings"
	"testing"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/testserver"
)

// A finished download is marked as from the internet, like a browser's, so
// Windows warns before running it. The address goes in without its query.
func TestFinishedFileIsMarked(t *testing.T) {
	srv, _ := rangeServer(t, testserver.Content(1<<20), nil)
	e, stopped := testEngine(t, nil)
	e.download(probe.Request{URL: srv.URL + "/file.bin?token=secret", Referer: "https://example.com/page?id=1"})
	<-stopped
	d := e.dl.list()[0]
	if d.State != fetch.Done {
		t.Fatalf("download: %+v", d)
	}
	b, err := os.ReadFile(d.Path + ":Zone.Identifier")
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"ZoneId=3\r\n", "HostUrl=" + srv.URL + "/file.bin\r\n", "ReferrerUrl=https://example.com/page\r\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "secret") {
		t.Errorf("the query went in: %q", got)
	}
}
