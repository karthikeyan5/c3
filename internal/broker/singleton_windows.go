//go:build windows

package broker

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// SingletonLock holds an exclusive-write handle on the pid file. On Windows the
// handle is opened with FILE_SHARE_READ only, so a second broker's GENERIC_WRITE
// open fails with a sharing violation (the single-broker guarantee); pure readers
// (status/pid) can still open it. The OS releases the handle on process death, so
// a crashed broker leaves no stale lock.
type SingletonLock struct {
	f       *os.File
	pidFile string
}

// AcquireSingleton ensures only one broker runs. The unix flock is replaced by a
// Windows share-mode lock: opening the pid file with FILE_SHARE_READ (no
// FILE_SHARE_WRITE) makes a second broker's GENERIC_WRITE open fail, which is the
// single-broker guarantee. See the SingletonLock doc for the crash-safety story.
func AcquireSingleton(pidFile string) (*SingletonLock, error) {
	if err := ensureParentDir(pidFile); err != nil {
		return nil, fmt.Errorf("ensure pid-file dir: %w", err)
	}
	p, err := syscall.UTF16PtrFromString(pidFile)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE, syscall.FILE_SHARE_READ,
		nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, fmt.Errorf("broker already running (pid file %s held): %w", pidFile, err)
	}
	f := os.NewFile(uintptr(h), pidFile)
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, fmt.Errorf("truncate pid file: %w", err)
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("write pid: %w", err)
	}
	return &SingletonLock{f: f, pidFile: pidFile}, nil
}

// Release closes the exclusive handle (which releases the lock) and removes the
// pid file.
func (s *SingletonLock) Release() {
	if s == nil {
		return
	}
	if s.f != nil {
		_ = s.f.Close()
	}
	_ = os.Remove(s.pidFile)
}
