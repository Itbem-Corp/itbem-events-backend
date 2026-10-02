//go:build unix

package automationagent

import (
	"os"
	"syscall"
)

func openSandboxSourceFile(root *os.Root, relative string) (*os.File, error) {
	// Confinement prevents ancestor escapes; nofollow and nonblock prevent a
	// swapped leaf from following a symlink or blocking on an injected FIFO.
	return root.OpenFile(relative, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
