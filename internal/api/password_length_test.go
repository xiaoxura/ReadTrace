package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestPasswordsRejectMoreThanBcryptByteLimit(t *testing.T) {
	for _, password := range []string{strings.Repeat("a", 73), strings.Repeat("中", 25)} {
		s := newAuthSecurityServer(t)
		body, _ := json.Marshal(map[string]string{"password": password})
		w := httptest.NewRecorder()
		s.handleSetup(w, httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(body)))
		hash, err := s.st.GetSetting(keyPasswordHash)
		if err != nil || hash != "" || w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "72") {
			t.Fatalf("oversized setup: %d %s hash set=%v err=%v", w.Code, w.Body.String(), hash != "", err)
		}
		initial, err := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.st.SetSetting(keyPasswordHash, string(initial)); err != nil {
			t.Fatal(err)
		}
		body, _ = json.Marshal(map[string]string{"current": "old-password", "new": password})
		w = httptest.NewRecorder()
		s.handlePasswordChange(w, httptest.NewRequest(http.MethodPost, "/api/settings/password", bytes.NewReader(body)))
		hash, err = s.st.GetSetting(keyPasswordHash)
		if err != nil || hash != string(initial) || w.Code != http.StatusBadRequest {
			t.Fatalf("oversized change: %d %s changed=%v err=%v", w.Code, w.Body.String(), hash != string(initial), err)
		}
	}
}

func TestSetupAcceptsSeventyTwoBytePassword(t *testing.T) {
	s := newAuthSecurityServer(t)
	password := strings.Repeat("中", 24)
	body, _ := json.Marshal(map[string]string{"password": password})
	w := httptest.NewRecorder()
	s.handleSetup(w, httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(body)))
	hash, err := s.st.GetSetting(keyPasswordHash)
	if w.Code != http.StatusOK || err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		t.Fatalf("72-byte setup: %d %s err=%v", w.Code, w.Body.String(), err)
	}
}
