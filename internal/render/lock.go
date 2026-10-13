package render

import (
	"os"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/distill"
)

// renderLockRetries and renderLockDelay bound how long withRenderLock waits
// for state/render.lock before giving up (req 3: "a bounded retry
// (20 × 50 ms)"). A busy lock is never an error — the caller simply skips its
// work this time; the next Sync (auto-render fires on every mutation) or the
// next explicit ReadBack picks it up.
const (
	renderLockRetries = 20
	renderLockDelay   = 50 * time.Millisecond
)

// withRenderLock takes state/render.lock (the same file-lock primitive
// distill.AcquireLearnLock uses for state/learn.lock) and runs fn while
// holding it. Both the exported ReadBack and Sync go through here, so a
// hook-triggered read-back can never interleave with another process's
// render (the audit's correctness gap 1). When the lock stays busy for the
// whole retry window, fn never runs and withRenderLock returns nil — a skip,
// not a failure.
func withRenderLock(cfg config.Config, fn func() error) error {
	// The lock file's parent must exist before O_CREATE|O_EXCL can create it.
	// Every normal entry point (openDB/EnsureLayout, the MCP server's
	// openStore) already creates state/, but render-package unit tests open
	// storage directly, so this is defensive rather than redundant.
	if err := os.MkdirAll(cfg.StateDir(), 0700); err != nil {
		return err
	}
	lockPath := cfg.StatePath("render.lock")
	release, ok := acquireRenderLock(lockPath)
	if !ok {
		return nil
	}
	defer release()
	return fn()
}

func acquireRenderLock(lockPath string) (release func(), ok bool) {
	for i := 0; i < renderLockRetries; i++ {
		if release, ok := distill.AcquireLearnLock(lockPath); ok {
			return release, true
		}
		if i < renderLockRetries-1 {
			time.Sleep(renderLockDelay)
		}
	}
	return func() {}, false
}
