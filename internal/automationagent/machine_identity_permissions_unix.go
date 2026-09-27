//go:build !windows

package automationagent

import (
	"fmt"
	"os"
)

func defaultMachineIdentityBaseDirectory() (string, error) { return os.UserConfigDir() }

func secureMachineIdentityDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("machine identity path is not a local directory")
	}
	return os.Chmod(path, 0o700)
}

func secureMachineIdentityFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("machine identity path is not a regular file")
	}
	return os.Chmod(path, 0o600)
}

func syncMachineIdentityDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
