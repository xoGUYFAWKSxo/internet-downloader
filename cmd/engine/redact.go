package main

import (
	"io"
	"net/url"
	"regexp"
)

// redacted keeps full addresses out of the log, which people are asked to
// attach to bug reports: an address can carry a token in its query or path,
// and Go's network errors quote the whole of it. The scheme and host stay.
type redacted struct{ w io.Writer }

var addressRE = regexp.MustCompile(`(?i)\b(?:https?|wss?)://[^\s"'<>]+`)

func (r redacted) Write(p []byte) (int, error) {
	out := addressRE.ReplaceAllFunc(p, func(m []byte) []byte {
		u, err := url.Parse(string(m))
		if err != nil || u.Host == "" {
			return []byte("[address]")
		}
		s := u.Scheme + "://" + u.Host
		if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			s += "/…"
		}
		return []byte(s)
	})
	if _, err := r.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}
