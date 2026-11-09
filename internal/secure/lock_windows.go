//go:build windows

package secure

import (
	"os"
	"syscall"
)

func openDataDirLock(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Share mode zero makes this handle exclusive. Windows releases it when
	// the process exits, including abnormal termination.
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

func closeDataDirLock(f *os.File) error { return f.Close() }
