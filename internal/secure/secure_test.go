package secure

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentFirstKeyCreationUsesOneKey(t *testing.T) {
	dir := t.TempDir()
	boxes := make([]*Box, 16)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range boxes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			box, err := New(dir)
			if err != nil {
				t.Errorf("New(%d): %v", i, err)
				return
			}
			boxes[i] = box
		}(i)
	}
	close(start)
	wg.Wait()
	for i, box := range boxes {
		if box == nil {
			t.Fatalf("box %d missing", i)
		}
	}
	plain := []byte("cookie credential")
	ciphertext, err := boxes[0].Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	for i, box := range boxes {
		got, err := box.Open(ciphertext)
		if err != nil || !bytes.Equal(got, plain) {
			t.Errorf("box %d cannot decrypt winner's key: %v", i, err)
		}
	}
	key, err := os.ReadFile(filepath.Join(dir, "secret.key"))
	if err != nil || len(key) != 32 {
		t.Fatalf("key size = %d, err = %v", len(key), err)
	}
}

func TestDataDirLockExclusiveAndReleasable(t *testing.T) {
	dir := t.TempDir()
	first, err := LockDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := LockDataDir(dir); err == nil {
		_ = second.Close()
		t.Fatal("second instance acquired occupied data directory")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := LockDataDir(dir)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
}
