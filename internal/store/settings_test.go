package store

import (
	"reflect"
	"testing"
)

func TestSetSettingsRollsBackFailedBatch(t *testing.T) {
	s := newTestStore(t)
	old := map[string]string{"site_name": "before", "timezone": "UTC"}
	if err := s.SetSettings(old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_timezone BEFORE INSERT ON settings
		WHEN NEW.key = 'timezone' BEGIN SELECT RAISE(ABORT, 'injected write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSettings(map[string]string{"site_name": "after", "timezone": "Asia/Shanghai"}); err == nil {
		t.Fatal("failed setting write unexpectedly succeeded")
	}
	got, err := s.AllSettings()
	if err != nil || !reflect.DeepEqual(got, old) {
		t.Fatalf("batch partially saved: %v err=%v", got, err)
	}
}
