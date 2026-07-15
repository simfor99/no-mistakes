package supervision

import (
	"fmt"
	"os"
)

type storeLock struct{ file *os.File }

func acquireStoreLock(path string) (*storeLock, bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open supervision lock: %w", err)
	}
	if err := tryLockStoreFile(file); err != nil {
		_ = file.Close()
		return nil, false, nil
	}
	return &storeLock{file: file}, true, nil
}

func (l *storeLock) Release() {
	if l == nil || l.file == nil {
		return
	}
	_ = unlockStoreFile(l.file)
	_ = l.file.Close()
	l.file = nil
}
