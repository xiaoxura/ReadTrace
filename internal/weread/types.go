package weread

import "net/http"

// Cookies is a simple name/value cookie jar used for the WeRead session.
type Cookies map[string]string

// MergeSetCookies copies Set-Cookie values into the jar.
func (c Cookies) MergeSetCookies(cs []*http.Cookie) {
	for _, rc := range cs {
		if rc == nil || rc.Name == "" || rc.Value == "" {
			continue
		}
		c[rc.Name] = rc.Value
	}
}
