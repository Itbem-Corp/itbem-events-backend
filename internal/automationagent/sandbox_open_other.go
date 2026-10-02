//go:build !unix

package automationagent

import "os"

func openSandboxSourceFile(root *os.Root, relative string) (*os.File, error) {
	return root.Open(relative)
}
