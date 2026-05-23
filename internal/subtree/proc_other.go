//go:build !unix

package subtree

import "syscall"

// newSeshProcAttr is a no-op on non-unix platforms (Windows etc.).
// The orch-spawn / sesh stack does not currently target those, but
// keeping the build-tagged stub means `go build ./...` does not
// break on hypothetical cross-builds.
func newSeshProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
