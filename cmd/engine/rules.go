package main

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// cleanSetting tidies the settings typed as lists, or says what is wrong.
var cleanSetting = map[string]func(string) (string, error){
	setCatchSites: cleanSites,
	setCatchTypes: cleanTypes,
}

func listItems(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	})
}

// cleanSites turns what was typed ("https://www.example.com/page",
// "*.example.com", "example.com") into host names, one per line.
func cleanSites(s string) (string, error) {
	var out []string
	seen := map[string]bool{}
	for _, item := range listItems(s) {
		h := strings.ToLower(item)
		if i := strings.Index(h, "://"); i >= 0 {
			h = h[i+3:]
		}
		h = strings.TrimPrefix(h, "*.")
		h = strings.Trim(strings.SplitN(h, "/", 2)[0], ".")
		if i := strings.LastIndex(h, ":"); i >= 0 {
			h = h[:i]
		}
		if !hostLike(h) {
			return "", fmt.Errorf("%q doesn't look like a site address, such as example.com.", item)
		}
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	if len(out) > 500 {
		return "", errors.New("That's too many sites; the list holds up to 500.")
	}
	return strings.Join(out, "\n"), nil
}

func hostLike(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, r := range h {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}

// cleanTypes turns "PDF, .mp3 *.zip" into "pdf mp3 zip".
func cleanTypes(s string) (string, error) {
	var out []string
	seen := map[string]bool{}
	for _, item := range listItems(s) {
		t := strings.TrimLeft(strings.ToLower(item), "*.")
		ok := t != "" && len(t) <= 16
		for _, r := range t {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				ok = false
			}
		}
		if !ok {
			return "", fmt.Errorf("%q doesn't look like a file type, such as pdf or mp3.", item)
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) > 200 {
		return "", errors.New("That's too many file types; the list holds up to 200.")
	}
	return strings.Join(out, " "), nil
}

// leftToBrowser is why a download the browser started stays with it under
// the user's rules, checked before asking the server; "" when it doesn't.
// Links sent from the right-click menu don't come here.
func (e *engine) leftToBrowser(addrs ...string) string {
	sites := strings.Fields(e.setting(setCatchSites))
	for _, a := range addrs {
		u, err := url.Parse(a)
		if err != nil || u.Hostname() == "" {
			continue
		}
		h := strings.ToLower(u.Hostname())
		for _, s := range sites {
			if h == s || strings.HasSuffix(h, "."+s) {
				return "site " + s
			}
		}
	}
	if len(addrs) > 0 {
		if u, err := url.Parse(addrs[0]); err == nil {
			if why := e.typeLeft(path.Base(u.Path)); why != "" {
				return why
			}
		}
	}
	return ""
}

// typeLeft says whether a file name's type is one the user leaves to the
// browser.
func (e *engine) typeLeft(name string) string {
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
	if ext == "" {
		return ""
	}
	for _, t := range strings.Fields(e.setting(setCatchTypes)) {
		if t == ext {
			return "file type " + t
		}
	}
	return ""
}

// catchMin is the smallest browser download the app takes: the user's
// setting or the extension's, whichever is larger.
func (e *engine) catchMin(fromExtension int64) int64 {
	mb, _ := strconv.ParseInt(e.setting(setCatchMin), 10, 64)
	return max(fromExtension, mb<<20)
}
