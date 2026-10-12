package main

import "testing"

func TestCallerFromArgs(t *testing.T) {
	cases := []struct {
		args []string
		want caller
	}{
		{[]string{"chrome-extension://abcdefghijklmnopabcdefghijklmnop/", "--parent-window=0"},
			caller{"chromium", "abcdefghijklmnopabcdefghijklmnop"}},
		{[]string{`C:\Users\x\AppData\Local\InternetDownloader\firefox-host.json`, "internet-downloader@xoguyfawksxo"},
			caller{"firefox", "internet-downloader@xoguyfawksxo"}},
		{nil, caller{Browser: "unknown"}},
		{[]string{"something-else"}, caller{Browser: "unknown"}},
	}
	for _, tc := range cases {
		if got := callerFromArgs(tc.args); got != tc.want {
			t.Errorf("callerFromArgs(%q) = %+v, want %+v", tc.args, got, tc.want)
		}
	}
}
