package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"readtrace/internal/engine"
	"readtrace/internal/secure"
	"readtrace/internal/store"
)

func TestAccountDailyGoalAPIAndSkippedStart(t *testing.T) {
	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "api.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetSetting("timezone", "UTC"); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateAccount(store.Account{Name: "api", DailyGoalMinutes: 30}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, nil, nil, engine.NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := &Server{st: st, eng: eng}
	s.tz.Store(time.UTC)
	update := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
		r.SetPathValue("id", fmt.Sprint(id))
		w := httptest.NewRecorder()
		s.handleAccountUpdate(w, r)
		return w
	}
	for _, body := range []string{`{"daily_goal_minutes":-1}`, `{"daily_goal_minutes":1441}`} {
		if w := update(body); w.Code != http.StatusBadRequest {
			t.Fatalf("%s => %d", body, w.Code)
		}
	}
	if w := update(`{"daily_goal_minutes":0}`); w.Code != http.StatusOK {
		t.Fatalf("disable => %d: %s", w.Code, w.Body.String())
	}
	acc, err := st.GetAccount(id)
	if err != nil || acc.DailyGoalMinutes != 0 {
		t.Fatalf("disabled account=%+v err=%v", acc, err)
	}
	if w := update(`{"daily_goal_minutes":30}`); w.Code != http.StatusOK {
		t.Fatalf("enable => %d: %s", w.Code, w.Body.String())
	}
	today := time.Now().UTC()
	startAt := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 1, 0, time.UTC).Unix()
	run := &store.Run{AccountID: id, Status: "failed", PlanSeconds: 1800,
		ElapsedSeconds: 1800, StartedAt: startAt, FinishedAt: startAt + 1800}
	if err := st.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.SetPathValue("id", fmt.Sprint(id))
	s.handleAccountGet(get, request)
	if get.Code != http.StatusOK {
		t.Fatalf("get => %d", get.Code)
	}
	var got store.Account
	if err := json.Unmarshal(get.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.TodaySeconds != 1800 || got.DailyRemainingSeconds != 0 {
		t.Fatalf("account counters=%+v", got)
	}
	list := httptest.NewRecorder()
	s.handleAccounts(list, httptest.NewRequest(http.MethodGet, "/", nil))
	var listed struct {
		Accounts []store.Account `json:"accounts"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if list.Code != http.StatusOK || len(listed.Accounts) != 1 || listed.Accounts[0].TodaySeconds != 1800 {
		t.Fatalf("list => %d %+v", list.Code, listed)
	}
	start := httptest.NewRecorder()
	s.handleRunStart(start, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(fmt.Sprintf(`{"account_id":%d,"minutes":30}`, id))))
	var resp map[string]any
	if err := json.Unmarshal(start.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if start.Code != http.StatusOK || resp["skipped"] != true || resp["reason"] != "今日目标已完成" {
		t.Fatalf("goal start => %d %+v", start.Code, resp)
	}
}
