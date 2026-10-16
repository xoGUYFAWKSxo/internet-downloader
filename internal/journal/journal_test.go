package journal

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/outfile"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
)

func TestJournal(t *testing.T) {
	// A space and an apostrophe, like many Windows user folders.
	path := filepath.Join(t.TempDir(), "Bob's files", "downloads.db")
	if err := mkdir(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	req := &probe.Request{URL: "https://example.test/a.mp4", Cookies: []media.Cookie{{Name: "s", Value: "secret"}}}
	if err := j.Add(fetch.Status{ID: 1, Name: "a.mp4", State: fetch.Downloading}, req); err != nil {
		t.Fatal(err)
	}
	j.Add(fetch.Status{ID: 2, Name: "b.mp4", State: fetch.Checking}, nil)
	j.SetFile(1, `C:\x\a.mp4.part`, 100, `"v1"`)
	have := []outfile.Range{{Start: 0, End: 40}, {Start: 60, End: 100}}
	if err := j.Commit(1, have); err != nil {
		t.Fatal(err)
	}
	j.Close()

	j, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	recs, err := j.Load()
	if err != nil || len(recs) != 2 {
		t.Fatalf("load: %v %d", err, len(recs))
	}
	r := recs[0]
	if r.Status.ID != 1 || r.Status.Name != "a.mp4" || r.Part != `C:\x\a.mp4.part` || r.Size != 100 || r.Validator != `"v1"` ||
		!reflect.DeepEqual(r.Have, have) || r.Request == nil || r.Request.Cookies[0].Value != "secret" {
		t.Fatalf("record = %+v", r)
	}
	if recs[1].Request != nil || recs[1].Stream != "" {
		t.Fatal("a stream kept a request")
	}
	if err := j.SetStream(2, `{"media":{"cookies":[{"name":"s","value":"secret"}]}}`); err != nil {
		t.Fatal(err)
	}
	if recs, _ = j.Load(); !strings.Contains(recs[1].Stream, "secret") {
		t.Fatalf("stream state %q", recs[1].Stream)
	}

	// Ending a download wipes its request and progress.
	j.End(fetch.Status{ID: 1, Name: "a.mp4", State: fetch.Done})
	j.End(fetch.Status{ID: 2, Name: "b.mp4", State: fetch.Cancelled})
	recs, _ = j.Load()
	if r := recs[0]; r.Request != nil || r.Part != "" || r.Have != nil || r.Status.State != fetch.Done {
		t.Fatalf("after End: %+v", r)
	}
	if recs[1].Stream != "" {
		t.Fatalf("a stream's state kept after End: %q", recs[1].Stream)
	}
}

func TestTrim(t *testing.T) {
	j, err := Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for id := uint64(1); id <= 5; id++ {
		var r *probe.Request
		if id == 1 {
			r = &probe.Request{URL: "https://example.test/"} // can still resume: kept
		}
		j.Add(fetch.Status{ID: id}, r)
	}
	j.SetStream(2, `{}`) // a paused stream: kept too
	j.Trim(2)
	recs, _ := j.Load()
	var ids []uint64
	for _, r := range recs {
		ids = append(ids, r.Status.ID)
	}
	if !reflect.DeepEqual(ids, []uint64{1, 2, 4, 5}) {
		t.Fatalf("kept %v", ids)
	}
}

func mkdir(p string) error { return os.MkdirAll(p, 0o755) }

// A list saved before downloads had a chosen folder and name opens, gains
// the columns, and keeps them and the settings across opening again.
func TestTargetAndSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "downloads.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE downloads (id INTEGER PRIMARY KEY, status TEXT NOT NULL, request TEXT NOT NULL DEFAULT '',
		part TEXT NOT NULL DEFAULT '', size INTEGER NOT NULL DEFAULT -1, validator TEXT NOT NULL DEFAULT '');
		INSERT INTO downloads (id, status) VALUES (1, '{"name":"old.bin","state":"done"}')`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	for i := 0; i < 2; i++ {
		j, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if got := j.Setting("ask", "1"); got != "1" {
				t.Fatalf("unset setting = %q", got)
			}
			j.Add(fetch.Status{ID: 2, Name: "b.bin", State: fetch.Paused}, &probe.Request{URL: "https://example.test/b.bin"})
			if err := j.SetTarget(2, `D:\Films`, "b.bin"); err != nil {
				t.Fatal(err)
			}
			j.SetSetting("ask", "0")
			j.SetSetting("ask", "1")
			j.SetSetting("ask", "0")
		}
		recs, err := j.Load()
		if err != nil || len(recs) != 2 || recs[0].Status.Name != "old.bin" || recs[0].Dir != "" || recs[1].Dir != `D:\Films` || recs[1].Name != "b.bin" {
			t.Fatalf("open %d: %v %+v", i, err, recs)
		}
		if got := j.Setting("ask", "1"); got != "0" {
			t.Fatalf("open %d: setting = %q", i, got)
		}
		j.Close()
	}
}

// Found streams survive closing, newest kept, and go after their time.
func TestReceived(t *testing.T) {
	path := filepath.Join(t.TempDir(), "downloads.db")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	for i := uint64(1); i <= 4; i++ {
		it := media.Item{ID: i, Received: now.Add(time.Duration(i) * time.Hour), Browser: "chrome",
			Context: media.Context{URL: "https://cdn.test/a.m3u8", Kind: media.KindHLS, Cookies: []media.Cookie{{Name: "s", Value: "secret"}}}}
		if err := j.SaveReceived(it, 3, now); err != nil {
			t.Fatal(err)
		}
	}
	j.Close()

	j, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	got, err := j.LoadReceived(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != 2 || got[2].ID != 4 || got[0].Context.Cookies[0].Value != "secret" || !got[0].Received.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("got %+v", got)
	}
	// Older than since: deleted, not just left out.
	if got, _ = j.LoadReceived(now.Add(3*time.Hour + time.Minute)); len(got) != 1 || got[0].ID != 4 {
		t.Fatalf("after expiry %+v", got)
	}
	if got, _ = j.LoadReceived(now); len(got) != 1 {
		t.Fatalf("expired ones came back: %+v", got)
	}
	// Saving one also deletes those past their time.
	j.SaveReceived(media.Item{ID: 6, Received: now.Add(5 * time.Hour)}, 3, now.Add(5*time.Hour))
	if got, _ = j.LoadReceived(now); len(got) != 1 || got[0].ID != 6 {
		t.Fatalf("after saving another: %+v", got)
	}
}
