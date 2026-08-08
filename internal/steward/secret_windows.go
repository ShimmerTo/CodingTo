//go:build windows

package steward

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

type dpapiCipher struct{}

// newSecretCipher returns the Windows DPAPI cipher. No key file is used; the
// current-user DPAPI scope binds the blobs to this Windows account.
func newSecretCipher(dir string) (secretCipher, error) {
	return dpapiCipher{}, nil
}

func (dpapiCipher) protect(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errors.New("secret is empty")
	}
	in := windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...), nil
}

func (dpapiCipher) unprotect(protected []byte) ([]byte, error) {
	if len(protected) == 0 {
		return nil, errors.New("protected secret is empty")
	}
	in := windows.DataBlob{Size: uint32(len(protected)), Data: &protected[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...), nil
}
