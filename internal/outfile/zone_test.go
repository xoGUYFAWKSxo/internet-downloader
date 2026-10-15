package outfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestZoneText(t *testing.T) {
	got := zoneText("https://user:pw@dl.example.com/files/setup.exe?token=secret#x", "https://example.com/page?id=1")
	want := "[ZoneTransfer]\r\nZoneId=3\r\nReferrerUrl=https://example.com/page\r\nHostUrl=https://dl.example.com/files/setup.exe\r\n"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got := zoneText("", "blob:https://example.com/1"); got != "[ZoneTransfer]\r\nZoneId=3\r\n" {
		t.Errorf("without addresses: got %q", got)
	}
}

func TestMarkFromInternet(t *testing.T) {
	if !zonePolicyOn() {
		t.Skip("a policy on this computer turns zone information off")
	}
	p := filepath.Join(t.TempDir(), "setup.exe")
	if err := os.WriteFile(p, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MarkFromInternet(p, "https://dl.example.com/setup.exe?sig=abc", ""); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p + ":Zone.Identifier")
	if err != nil {
		t.Fatal(err)
	}
	if want := "[ZoneTransfer]\r\nZoneId=3\r\nHostUrl=https://dl.example.com/setup.exe\r\n"; string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if b, _ := os.ReadFile(p); string(b) != "MZ" {
		t.Errorf("the file itself changed: %q", b)
	}
	gone := filepath.Join(t.TempDir(), "gone.exe")
	if err := MarkFromInternet(gone, "https://dl.example.com/gone.exe", ""); err == nil {
		t.Error("marked a missing file")
	}
	if _, err := os.Stat(gone); err == nil {
		t.Error("marking a missing file created it")
	}
}
