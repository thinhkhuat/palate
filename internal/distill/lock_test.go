package distill

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLearnLockRefusesSecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learn.lock")

	release, ok := AcquireLearnLock(path)
	if !ok {
		t.Fatal("first acquire must succeed")
	}
	defer release()

	if _, ok := AcquireLearnLock(path); ok {
		t.Fatal("second acquire against a fresh lock must fail")
	}
}

func TestLearnLockReleaseAllowsReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learn.lock")

	release, ok := AcquireLearnLock(path)
	if !ok {
		t.Fatal("first acquire must succeed")
	}
	release()

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("release must remove the lock file, stat err=%v", err)
	}

	release2, ok := AcquireLearnLock(path)
	if !ok {
		t.Fatal("acquire after release must succeed")
	}
	release2()
}

func TestLearnLockStaleTakeover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learn.lock")

	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}
	old := time.Now().Add(-learnLockStaleAfter - time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	release, ok := AcquireLearnLock(path)
	if !ok {
		t.Fatal("acquire must take over a stale lock")
	}
	defer release()

	matches, err := filepath.Glob(path + ".taken-*")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("stale takeover left an orphan file behind: %v", matches)
	}
}

func TestLearnLockFreshLockIsNotStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learn.lock")

	release, ok := AcquireLearnLock(path)
	if !ok {
		t.Fatal("first acquire must succeed")
	}
	defer release()

	if _, ok := AcquireLearnLock(path); ok {
		t.Fatal("a lock younger than the stale window must not be taken over")
	}
}

// TestLearnLockConcurrentStaleTakeover races several goroutines against the
// same stale lock: exactly one must win the rename-based takeover.
func TestLearnLockConcurrentStaleTakeover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learn.lock")

	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}
	old := time.Now().Add(-learnLockStaleAfter - time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	const racers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	var releases []func()
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if release, ok := AcquireLearnLock(path); ok {
				mu.Lock()
				releases = append(releases, release)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(releases) != 1 {
		t.Fatalf("expected exactly 1 winner of the stale takeover race, got %d", len(releases))
	}
	releases[0]()
}

// A fresh lock must stay on disk without interruption while contenders
// probe it: a live holder never sees its lock file vanish.
func TestLearnLockFreshLockNeverLeavesDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learn.lock")
	release, ok := AcquireLearnLock(path)
	if !ok {
		t.Fatal("initial acquire failed")
	}
	defer release()

	stop := make(chan struct{})
	var missing atomic.Int32
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := os.Stat(path); err != nil {
					missing.Add(1)
				}
			}
		}
	}()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if _, got := AcquireLearnLock(path); got {
					t.Error("a contender acquired a fresh lock")
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	if n := missing.Load(); n != 0 {
		t.Fatalf("lock file was missing from disk %d times while held", n)
	}
}
