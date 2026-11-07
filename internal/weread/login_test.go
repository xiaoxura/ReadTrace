package weread

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(handler http.HandlerFunc) (*Client, *httptest.Server) {
	srv := httptest.NewServer(handler)
	c := NewClient()
	c.Base = srv.URL
	return c, srv
}

func TestLoginPollLoginSuccessWithCookie(t *testing.T) {
	c, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/getLoginInfo" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("otp") != "" {
			t.Errorf("otp should be present but empty")
		}
		http.SetCookie(w, &http.Cookie{Name: "wr_vid", Value: "vid-from-cookie"})
		http.SetCookie(w, &http.Cookie{Name: "wr_skey", Value: "skey-from-cookie"})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"succeed":     false,
			"logicCode":   "LOGIN_SUCCESS",
			"accessToken": "access-token-123",
		})
	})
	defer srv.Close()

	res, err := c.LoginPoll(context.Background(), "uid-1", "")
	if err != nil {
		t.Fatalf("LoginPoll: %v", err)
	}
	if !res.Success {
		t.Fatal("LOGIN_SUCCESS should be treated as success")
	}
	if res.AccessToken != "access-token-123" {
		t.Fatalf("token = %q", res.AccessToken)
	}
	if res.Vid != "vid-from-cookie" {
		t.Fatalf("vid = %q, want cookie value", res.Vid)
	}
	if res.Cookies["wr_vid"] != "vid-from-cookie" || res.Cookies["wr_skey"] != "skey-from-cookie" {
		t.Fatalf("cookies not captured: %#v", res.Cookies)
	}
}

func TestLoginPollExpiredSession(t *testing.T) {
	c, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"statusCode":401,"data":{"errcode":-2013}}`))
	})
	defer srv.Close()

	_, err := c.LoginPoll(context.Background(), "uid-2", "")
	if !errors.Is(err, ErrLoginSessionExpired) {
		t.Fatalf("err = %v, want ErrLoginSessionExpired", err)
	}
}

func TestLoginPollTimeoutIsNotSuccess(t *testing.T) {
	c, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"succeed":false,"logicCode":"LOGIN_TIMEOUT","canRetry":false}`))
	})
	defer srv.Close()

	res, err := c.LoginPoll(context.Background(), "uid-3", "")
	if err != nil {
		t.Fatalf("LoginPoll: %v", err)
	}
	if res.Success {
		t.Fatal("LOGIN_TIMEOUT must not be success")
	}
	if res.LogicCode != "LOGIN_TIMEOUT" {
		t.Fatalf("logic = %q", res.LogicCode)
	}
}
