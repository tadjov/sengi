package testcontainer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/testcontainers/testcontainers-go"
)

// runScopedName names a container that every package of one go test run finds
// and reuses, and that no other run does: the testcontainers session is the go
// test invocation, and its reaper removes the container when the run ends.
func runScopedName(kind, variant string) string {
	sum := sha256.Sum256([]byte(variant))
	return fmt.Sprintf("sengi-test-%s-%s-%s", kind, hex.EncodeToString(sum[:4]), testcontainers.SessionID()[:12])
}

// runLock serialises a resource between the packages of one run, which go test
// runs as separate processes at once.
type runLock struct {
	mu      sync.Mutex
	holders int
	file    *os.File
}

func (l *runLock) acquire(name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holders > 0 {
		l.holders++
		return nil
	}
	file, err := os.OpenFile(filepath.Join(os.TempDir(), name+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock %s: %w", name, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return fmt.Errorf("lock %s: %w", name, err)
	}
	l.file, l.holders = file, 1
	return nil
}

func (l *runLock) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holders--; l.holders > 0 {
		return
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	_ = l.file.Close()
	l.file = nil
}
