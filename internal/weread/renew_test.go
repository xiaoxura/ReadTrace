package weread

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// Renew must pick up a fresh wr_skey even when the endpoint answers with a
// non-2xx status code — this happens in practice and previously marked valid
// sessions as expired.
func TestRenewCapturesCookieOnNon2xx(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		http.SetCookie(w, &http.Cookie{Name: "wr_skey", Value: "NEWKEY"})
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewClient()
	c.Base = srv.URL
	ck := Cookies{"wr_vid": "123", "wr_skey": "OLDKEY"}
	key, err := c.Renew(context.Background(), ck)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if gotPath != "/web/login/renewal" {
		t.Fatalf("expected renewal endpoint, got %s", gotPath)
	}
	if key != "NEWKEY" {
		t.Fatalf("expected NEWKEY, got %q", key)
	}
	if ck["wr_skey"] != "NEWKEY" {
		t.Fatalf("cookie jar not updated: %v", ck)
	}
}

// A response without a usable cookie keeps returning an error after all
// variants are exhausted.
func TestRenewFailsWithoutCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"succ":0}`))
	}))
	defer srv.Close()

	c := NewClient()
	c.Base = srv.URL
	ck := Cookies{"wr_vid": "123", "wr_skey": "OLDKEY"}
	if _, err := c.Renew(context.Background(), ck); err == nil {
		t.Fatal("renew should fail when no wr_skey is returned")
	}
}

// Validate must wrap ErrUnauthorized for true login failures (errCode
// -2012/-2013/401) so callers can distinguish them from transient errors.
func TestValidateClassifiesAuthErrors(t *testing.T) {
	cases := []struct {
		errCode       int
		wantError     bool
		wantAuthClass bool
	}{
		{errCode: -2012, wantError: true, wantAuthClass: true},
		{errCode: -2013, wantError: true, wantAuthClass: true},
		{errCode: 401, wantError: true, wantAuthClass: true},
		{errCode: 0, wantError: false},
		{errCode: 57, wantError: true, wantAuthClass: false},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"errCode":` + strconv.Itoa(tc.errCode) + `}`))
		}))
		c := NewClient()
		c.Base = srv.URL
		err := c.Validate(context.Background(), Cookies{})
		srv.Close()

		if (err != nil) != tc.wantError {
			t.Fatalf("errCode %d: wantError=%v got err=%v", tc.errCode, tc.wantError, err)
		}
		if tc.wantAuthClass != errors.Is(err, ErrUnauthorized) {
			t.Fatalf("errCode %d: auth classification mismatch, err=%v", tc.errCode, err)
		}
	}
}
