//go:build unix

package poollease

import (
	"errors"
	"os"
	"sync"
	"syscall"
)

// Lease serializes cross-process credential refresh ownership. It deliberately
// does not store credential material or infer ownership from a stale PID file.
type Lease struct {
	mu   sync.Mutex
	path string
	file *os.File
	lost bool
}

func New(path string) *Lease { return &Lease{path: path} }

func (l *Lease) TryAcquire() (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lost {
		return false, errors.New("pool refresh lease identity lost")
	}
	if l.file != nil {
		opened, errOpened := l.file.Stat()
		current, errCurrent := os.Lstat(l.path)
		if errOpened != nil || errCurrent != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
			l.lost = true
			_ = l.file.Close()
			l.file = nil
			return false, errors.New("pool refresh lease identity changed")
		}
		return true, nil
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return false, nil
		}
		return false, err
	}
	opened, errOpened := f.Stat()
	current, errCurrent := os.Lstat(l.path)
	if errOpened != nil || errCurrent != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		_ = f.Close()
		l.lost = true
		return false, errors.New("pool refresh lease identity changed")
	}
	l.file = f
	return true, nil
}

func (l *Lease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
