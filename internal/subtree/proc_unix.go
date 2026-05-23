//go:build unix

package subtree

import "syscall"

// newSeshProcAttr returns the SysProcAttr that detaches a child into a
// fresh process group so SIGINT to orch-subtree doesn't cascade to the
// sesh hub. Unix variant.
func newSeshProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
