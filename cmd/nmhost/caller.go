package main

import "strings"

// caller identifies which browser launched the host. Chromium browsers
// (Chrome, Edge) pass the extension origin as the first argument; Firefox
// passes the manifest path and then the extension ID.
type caller struct {
	Browser   string `json:"browser"` // "chromium", "firefox" or "unknown"
	Extension string `json:"extension,omitempty"`
}

func callerFromArgs(args []string) caller {
	if len(args) >= 1 && strings.HasPrefix(args[0], "chrome-extension://") {
		id := strings.TrimSuffix(strings.TrimPrefix(args[0], "chrome-extension://"), "/")
		return caller{Browser: "chromium", Extension: id}
	}
	if len(args) >= 2 && strings.HasSuffix(strings.ToLower(args[0]), ".json") {
		return caller{Browser: "firefox", Extension: args[1]}
	}
	return caller{Browser: "unknown"}
}
