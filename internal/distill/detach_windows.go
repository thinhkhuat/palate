//go:build windows

package distill

import "syscall"

// detachAttrs puts the child in its own process group so it survives the
// parent hook process exiting and is not delivered console events (e.g.
// Ctrl-C) meant for the parent's group. syscall.SysProcAttr's fields
// differ per GOOS, which is why this lives in its own build-tagged file
// rather than alongside detach_unix.go's Setsid.
func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
