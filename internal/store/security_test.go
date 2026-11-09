package store

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"readtrace/internal/secure"
)

func TestSetSettingIfEmptyConcurrent(t *testing.T) {
	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(filepath.Join(dir, "readtrace.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var wg sync.WaitGroup
	winners := make(chan string, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			value := string(rune('A' + i))
			won, err := st.SetSettingIfEmpty("password_hash", value)
			if err != nil {
				t.Errorf("set %d: %v", i, err)
			} else if won {
				winners <- value
			}
		}(i)
	}
	wg.Wait()
	close(winners)
	var winner string
	for value := range winners {
		if winner != "" {
			t.Fatal("more than one setup won")
		}
		winner = value
	}
	if winner == "" {
		t.Fatal("no setup won")
	}
	stored, err := st.GetSetting("password_hash")
	if err != nil || stored != winner {
		t.Fatalf("stored hash = %q, err = %v, winner = %q", stored, err, winner)
	}
}

func TestDatabaseFilesPrivateInExistingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file mode bits do not represent ACLs")
	}
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	box, err := secure.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "readtrace.db")
	for _, existing := range []bool{false, true} {
		if existing {
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			for _, suffix := range []string{"-wal", "-shm"} {
				if err := os.WriteFile(path+suffix, nil, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path+suffix, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		st, err := Open(path, box)
		if err != nil {
			t.Fatalf("open (existing=%v): %v", existing, err)
		}
		if err := st.SetSetting("permission_probe", time.Now().String()); err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			info, err := os.Stat(path + suffix)
			if err != nil {
				t.Errorf("stat %s (existing=%v): %v", suffix, existing, err)
				continue
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Errorf("%s mode = %04o, want 0600 (existing=%v)", suffix, got, existing)
			}
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
