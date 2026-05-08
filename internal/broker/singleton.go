package broker

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// SingletonLock represents a held flock. Caller must Release on shutdown.
type SingletonLock struct {
	fd      int
	pidFile string
}

// AcquireSingleton ensures only one broker runs. Spec §4.2.2:
//
//   - flock(LOCK_EX | LOCK_NB) on pidFile.
//   - On EWOULDBLOCK: read pid; if alive, return error (sibling won race).
//     If dead, unlink stale pid file and retry once.
//   - On success: write own pid, do NOT close fd (closing releases flock).
func AcquireSingleton(pidFile string) (*SingletonLock, error) {
	if err := ensureParentDir(pidFile); err != nil {
		return nil, fmt.Errorf("ensure pid-file dir: %w", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		fd, err := syscall.Open(pidFile, syscall.O_CREAT|syscall.O_RDWR, 0600)
		if err != nil {
			return nil, fmt.Errorf("open pid file %s: %w", pidFile, err)
		}

		if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = syscall.Close(fd)
			if !isLocked(err) {
				return nil, fmt.Errorf("flock %s: %w", pidFile, err)
			}
			alive, _ := pidAlive(pidFile)
			if alive {
				return nil, fmt.Errorf("broker already running (pid file %s held)", pidFile)
			}
			if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("remove stale pid file: %w", err)
			}
			continue
		}

		_ = syscall.Ftruncate(fd, 0)
		ourPid := os.Getpid()
		_, err = syscall.Write(fd, []byte(strconv.Itoa(ourPid)+"\n"))
		if err != nil {
			_ = syscall.Close(fd)
			return nil, fmt.Errorf("write pid: %w", err)
		}
		// Do NOT close fd — closing releases the flock.
		return &SingletonLock{fd: fd, pidFile: pidFile}, nil
	}

	return nil, fmt.Errorf("could not acquire singleton lock on %s", pidFile)
}

// Release releases the flock and removes the pid file.
func (s *SingletonLock) Release() {
	if s == nil {
		return
	}
	_ = syscall.Flock(s.fd, syscall.LOCK_UN)
	_ = syscall.Close(s.fd)
	_ = os.Remove(s.pidFile)
}

func isLocked(err error) bool {
	return err == syscall.EWOULDBLOCK || err == syscall.EAGAIN
}

func pidAlive(pidFile string) (bool, error) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false, err
	}
	pidStr := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return false, fmt.Errorf("invalid pid in file: %q", pidStr)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		if err == syscall.ESRCH {
			return false, nil
		}
		if err == syscall.EPERM {
			return true, nil
		}
		return false, err
	}
	return true, nil
}
