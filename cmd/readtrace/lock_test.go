package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"readtrace/internal/secure"
	"readtrace/internal/store"
)

func TestOccupiedDataDirCannotMutateRunOrPassword(t *testing.T) {
	dir := t.TempDir()
	lock, err := secure.LockDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "readtrace.db")
	st, err := store.Open(path, box)
	if err != nil {
		t.Fatal(err)
	}
	runRow := &store.Run{AccountID: 1, Trigger: "manual", PlanSeconds: 60, Status: "running", StartedAt: time.Now().Unix()}
	if err := st.CreateRun(runRow); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("password_hash", "sentinel"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run("127.0.0.1:0", dir, false, 90, "", logger); err == nil {
		t.Fatal("second server started with occupied data directory")
	}
	if err := resetPanelPassword(dir); err == nil {
		t.Fatal("password reset entered occupied data directory")
	}
	st, err = store.Open(path, box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.GetRun(runRow.ID)
	if err != nil || got == nil || got.Status != "running" {
		t.Fatalf("run status changed: %+v, err=%v", got, err)
	}
	hash, err := st.GetSetting("password_hash")
	if err != nil || hash != "sentinel" {
		t.Fatalf("password changed: %q, err=%v", hash, err)
	}
}
