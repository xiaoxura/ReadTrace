package weread

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTP401ClassifiedAsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("not logged in"))
	}))
	defer srv.Close()
	c := NewClient()
	c.Base = srv.URL
	if err := c.Validate(context.Background(), Cookies{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Validate HTTP 401 = %v", err)
	}
	if _, err := c.Read(context.Background(), Cookies{}, map[string]any{"b": "book"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Read HTTP 401 = %v", err)
	}
}

func TestRenewDistinguishesAuthRefusalFromTransientFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		auth   bool
	}{
		{"http401", http.StatusUnauthorized, "unauthorized", true},
		{"jsonAuth", http.StatusOK, `{"errCode":-2012}`, true},
		{"serverError", http.StatusServiceUnavailable, `{"succ":0}`, false},
		{"serverErrorWithAuthBody", http.StatusServiceUnavailable, `{"errCode":-2012}`, false},
		{"noCookie", http.StatusOK, `{"succ":0}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := NewClient()
			c.Base = srv.URL
			_, err := c.Renew(context.Background(), Cookies{"wr_skey": "old"})
			if err == nil || errors.Is(err, ErrUnauthorized) != tc.auth {
				t.Fatalf("Renew error = %v, want auth=%v", err, tc.auth)
			}
		})
	}
}
