//go:build !windows

package distill

import "syscall"

// detachAttrs puts the child in its own session (Setsid) so it survives
// the parent hook process exiting and is not delivered signals meant for
// the parent's process group.
func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
