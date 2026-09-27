//go:build windows

package automationagent

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DPAPI binds the local Ed25519 key to the current Windows user. The
// machine-wide scope is deliberately not requested.
func protectMachineIdentityKey(key []byte) ([]byte, error) {
	return transformMachineIdentityKey(key, true)
}

func unprotectMachineIdentityKey(key []byte) ([]byte, error) {
	return transformMachineIdentityKey(key, false)
}

func transformMachineIdentityKey(input []byte, protect bool) ([]byte, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("empty machine signing key")
	}
	inputBlob := windows.DataBlob{Size: uint32(len(input)), Data: &input[0]}
	var outputBlob windows.DataBlob
	var err error
	if protect {
		err = windows.CryptProtectData(&inputBlob, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &outputBlob)
	} else {
		err = windows.CryptUnprotectData(&inputBlob, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &outputBlob)
	}
	if err != nil || outputBlob.Data == nil || outputBlob.Size == 0 {
		return nil, fmt.Errorf("windows DPAPI could not protect local machine signing key")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(outputBlob.Data)))
	return append([]byte(nil), unsafe.Slice(outputBlob.Data, int(outputBlob.Size))...), nil
}
