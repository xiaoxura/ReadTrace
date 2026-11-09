package checker

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"readtrace/internal/store"
)

func TestTickResetsRunningAfterEveryPass(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "checker.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := New(st, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for i := 0; i < 2; i++ {
		c.lastRun = time.Time{}
		c.tick()
		if c.running || c.lastRun.IsZero() {
			t.Fatalf("pass %d: running=%v lastRun=%v", i, c.running, c.lastRun)
		}
	}
	_ = st.Close()
	c.lastRun = time.Time{}
	c.tick()
	if c.running {
		t.Fatal("running left set after database failure")
	}
}
