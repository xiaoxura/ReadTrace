package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"readtrace/internal/checker"
	"readtrace/internal/sched"
)

func TestSettingsValidationDoesNotPartiallySave(t *testing.T) {
	s := newCheckTestServer(t, http.NewServeMux())
	s.sch = sched.New(s.st, s.eng, s.log)
	s.setTimezone(time.UTC)
	old := map[string]string{keySiteName: "before", keyTimezone: "UTC", checker.SettingInterval: "60"}
	if err := s.st.SetSettings(old); err != nil {
		t.Fatal(err)
	}
	if err := s.sch.Start(time.UTC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.sch.Stop)
	for _, body := range []string{
		`{"site_name":"after","timezone":"Invalid/Zone","check_interval_minutes":0}`,
		`{"site_name":"after","timezone":"Asia/Shanghai","check_interval_minutes":1441}`,
	} {
		w := httptest.NewRecorder()
		s.handleSettingsUpdate(w, httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid settings: %d %s", w.Code, w.Body.String())
		}
		got, err := s.st.AllSettings()
		if err != nil || !reflect.DeepEqual(got, old) || s.timezone() != time.UTC {
			t.Fatalf("invalid request changed settings: %v tz=%v err=%v", got, s.timezone(), err)
		}
	}
	w := httptest.NewRecorder()
	s.handleSettingsUpdate(w, httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(
		`{"site_name":"after","timezone":"Asia/Shanghai","check_interval_minutes":0}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("valid settings: %d %s", w.Code, w.Body.String())
	}
	got, err := s.st.AllSettings()
	want := map[string]string{keySiteName: "after", keyTimezone: "Asia/Shanghai", checker.SettingInterval: "0"}
	if err != nil || !reflect.DeepEqual(got, want) || s.timezone().String() != "Asia/Shanghai" {
		t.Fatalf("saved=%v tz=%v err=%v", got, s.timezone(), err)
	}
}
