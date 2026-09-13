package distill

import (
	"fmt"
	"os"
	"time"
)

// learnLockStaleAfter is how long an unreleased lock file is treated as
// orphaned from a crashed run rather than a live one.
const learnLockStaleAfter = 10 * time.Minute

// AcquireLearnLock takes an exclusive, file-based lock at path so at most
// one `palate learn *` run touches the DB at a time. ok is false when
// another run already holds a fresh lock — not an error: overlapping
// learn attempts are expected under rapid hook firings, and the correct
// response is a silent no-op exit, not a failure.
func AcquireLearnLock(path string) (release func(), ok bool) {
	noop := func() {}

	if f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
		return func() { _ = os.Remove(path) }, true
	} else if !os.IsExist(err) {
		return noop, false
	}

	// A fresh lock is never touched: a live holder must never see its lock
	// vanish from disk, even briefly, or a third acquirer could slip in.
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) < learnLockStaleAfter {
		return noop, false
	}

	// Stale takeover: the holder is presumed dead. Capture whatever sits at
	// path by renaming it to a name unique to this attempt, THEN re-check its
	// mtime — a slower racer's rename would otherwise steal a lock an earlier
	// racer already re-created as fresh. os.Rename is atomic on the same
	// filesystem, so at most one racer captures a given generation of the
	// file; every later racer fails with ENOENT instead of racing ahead.
	takenPath := fmt.Sprintf("%s.taken-%d-%d", path, os.Getpid(), time.Now().UnixNano())
	if err := os.Rename(path, takenPath); err != nil {
		return noop, false
	}
	if info, err := os.Stat(takenPath); err != nil || time.Since(info.ModTime()) < learnLockStaleAfter {
		// The file was recreated fresh between our stat and our rename — hand
		// it back rather than seize it out from under its real holder.
		_ = os.Rename(takenPath, path)
		return noop, false
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		// Someone else recreated path between our capture and our create;
		// they own it now. Drop the captured stale copy.
		_ = os.Remove(takenPath)
		return noop, false
	}
	_ = f.Close()
	_ = os.Remove(takenPath)
	return func() { _ = os.Remove(path) }, true
}
