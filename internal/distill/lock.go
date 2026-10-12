package distill

import (
	"fmt"
	"os"
	"time"
)

// learnLockStaleAfter is how long an unreleased lock file is treated as
// orphaned from a crashed run rather than a live one.
const learnLockStaleAfter = 10 * time.Minute

// afterStaleGuardCheck runs between a racer deciding a takeover guard is stale
// and acting on it; a test seam for the race that window allows.
var afterStaleGuardCheck = func() {}

// AcquireLearnLock takes an exclusive, file-based lock at path so at most
// one `palate learn *` run touches the DB at a time. ok is false when
// another run already holds a fresh lock — not an error: overlapping
// learn attempts are expected under rapid hook firings, and the correct
// response is a silent no-op exit, not a failure.
func AcquireLearnLock(path string) (release func(), ok bool) {
	noop := func() {}

	if release, err := createLock(path); err == nil {
		return release, true
	} else if !os.IsExist(err) {
		return noop, false
	}

	// A fresh lock is never touched: a live holder must never see its lock
	// vanish from disk, even briefly, or a third acquirer could slip in.
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) < learnLockStaleAfter {
		return noop, false
	}

	// Stale takeover: the holder is presumed dead. Only one racer at a time
	// may touch the stale file, so the takeover runs under a short-lived guard
	// file created with O_EXCL. Every other racer backs off. The earlier
	// scheme renamed the lock away and handed it back if it turned out fresh,
	// but that handed-back window let a third racer create the lock while its
	// real holder believed it still had it (two winners).
	guard := path + ".takeover"
	g, err := os.OpenFile(guard, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		// Another racer is mid-takeover. A guard left behind by a crash
		// inside that window is cleared once it is itself stale, so the
		// next attempt can proceed.
		if gi, serr := os.Stat(guard); serr == nil && time.Since(gi.ModTime()) >= learnLockStaleAfter {
			clearStaleGuard(guard, gi)
		}
		return noop, false
	}
	_ = g.Close()
	defer func() { _ = os.Remove(guard) }()

	// Re-check under the guard: an earlier racer may already have replaced
	// the stale file with its own fresh lock, which must never be touched.
	if info, err := os.Stat(path); err == nil {
		if time.Since(info.ModTime()) < learnLockStaleAfter {
			return noop, false
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return noop, false
		}
	}
	release, err = createLock(path)
	if err != nil {
		// Someone created path between our remove and our create; they own
		// it now.
		return noop, false
	}
	return release, true
}

// clearStaleGuard removes the takeover guard seen was judged stale from. A
// plain os.Remove by name could delete a fresh guard another racer created
// after the check, so the guard is first renamed to a name of its own and only
// deleted if it is still the file that was seen; a different (fresh) file is
// put back.
func clearStaleGuard(guard string, seen os.FileInfo) {
	afterStaleGuardCheck()
	tomb := fmt.Sprintf("%s.stale-%d-%d", guard, os.Getpid(), time.Now().UnixNano())
	if os.Rename(guard, tomb) != nil {
		return
	}
	defer func() { _ = os.Remove(tomb) }()
	if now, err := os.Stat(tomb); err == nil && os.SameFile(seen, now) {
		return
	}
	// os.Link fails when someone has taken the path meanwhile, which is
	// then theirs.
	_ = os.Link(tomb, guard)
}

// createLock creates path exclusively and writes a token unique to this
// holder into it. The returned release removes the file only while it still
// holds that token: a holder that ran past the staleness bound and was taken
// over must not delete the new holder's lock when it finally finishes.
func createLock(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	token := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	_, werr := f.WriteString(token)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("write lock token: %v %v", werr, cerr)
	}
	return func() {
		if b, rerr := os.ReadFile(path); rerr == nil && string(b) == token {
			_ = os.Remove(path)
		}
	}, nil
}

// TouchLock refreshes a held lock's mtime to now, so a multi-batch dream
// pass that runs past learnLockStaleAfter is never mistaken for an orphaned,
// crashed holder by a concurrent stale-takeover attempt.
func TouchLock(path string) error {
	now := time.Now()
	return os.Chtimes(path, now, now)
}
