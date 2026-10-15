package outfile

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// MarkFromInternet tags a finished file as downloaded from the internet, as
// browsers do (the Zone.Identifier stream, "mark of the web"). Windows then
// warns before running it and Office opens it in Protected View. from and
// referrer go in without their query, which can hold tokens. Nothing is
// written when the "Do not preserve zone information" policy is on.
func MarkFromInternet(path, from, referrer string) error {
	if !zonePolicyOn() {
		return nil
	}
	// Writing the stream would create a missing file.
	if fi, err := os.Stat(path); err != nil {
		return err
	} else if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s isn't a file", path)
	}
	return os.WriteFile(path+":Zone.Identifier", []byte(zoneText(from, referrer)), 0o644)
}

func zoneText(from, referrer string) string {
	var b strings.Builder
	b.WriteString("[ZoneTransfer]\r\nZoneId=3\r\n")
	if r := bareURL(referrer); r != "" {
		b.WriteString("ReferrerUrl=" + r + "\r\n")
	}
	if u := bareURL(from); u != "" {
		b.WriteString("HostUrl=" + u + "\r\n")
	}
	return b.String()
}

// bareURL is an http(s) address without its user name, password, query or
// fragment; "" for anything else.
func bareURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	u.User, u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = nil, "", false, "", ""
	return u.String()
}

// zonePolicyOn is false when a policy says not to keep zone information
// (SaveZoneInformation = 1, user or machine).
func zonePolicyOn() bool {
	const key = `Software\Microsoft\Windows\CurrentVersion\Policies\Attachments`
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		k, err := registry.OpenKey(root, key, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := k.GetIntegerValue("SaveZoneInformation")
		k.Close()
		if err == nil && v == 1 {
			return false
		}
	}
	return true
}
