//go:build windows

package vault

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var keyHeader = []byte("TGWK1")

func protectKey(key []byte) ([]byte, error) {
	if len(key) == 0 {
		return nil, errors.New("cannot protect empty key")
	}
	input := windows.DataBlob{Size: uint32(len(key)), Data: &key[0]}
	var output windows.DataBlob
	name, _ := windows.UTF16PtrFromString("TG Workbench vault key")
	if err := windows.CryptProtectData(&input, name, nil, 0, nil, 0x1, &output); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	result := append([]byte(nil), keyHeader...)
	result = append(result, unsafe.Slice(output.Data, output.Size)...)
	return result, nil
}

func unprotectKey(stored []byte) ([]byte, error) {
	if len(stored) <= len(keyHeader) || string(stored[:len(keyHeader)]) != string(keyHeader) {
		return nil, errors.New("invalid vault key header")
	}
	ciphertext := stored[len(keyHeader):]
	input := windows.DataBlob{Size: uint32(len(ciphertext)), Data: &ciphertext[0]}
	var output windows.DataBlob
	if err := windows.CryptUnprotectData(&input, nil, nil, 0, nil, 0x1, &output); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	return append([]byte(nil), unsafe.Slice(output.Data, output.Size)...), nil
}
