//go:build !windows

package automationagent

// Unix relies on a private configuration directory and 0600 identity file.
// Copying prevents callers from aliasing the in-memory input buffer.
func protectMachineIdentityKey(key []byte) ([]byte, error) {
	return append([]byte(nil), key...), nil
}

func unprotectMachineIdentityKey(key []byte) ([]byte, error) {
	return append([]byte(nil), key...), nil
}
