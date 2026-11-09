package secure

import (
	"fmt"
	"os"
	"path/filepath"
)

// DataDirLock holds the single-instance lock for one data directory. Keep it
// open for the entire lifetime of the service; the OS releases it on a crash.
type DataDirLock struct {
	file *os.File
}

func LockDataDir(dataDir string) (*DataDirLock, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "readtrace.lock")
	f, err := openDataDirLock(path)
	if err != nil {
		return nil, fmt.Errorf("数据目录已被其他实例占用或无法加锁 (%s): %w", path, err)
	}
	return &DataDirLock{file: f}, nil
}

func (l *DataDirLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := closeDataDirLock(l.file)
	l.file = nil
	return err
}
