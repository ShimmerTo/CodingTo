//go:build !windows

package browserworkflow

import "errors"

func credentialStoreName() string { return "" }

func protectCredential([]byte) ([]byte, error) {
	return nil, errors.New("no supported operating-system credential store")
}

func unprotectCredential([]byte) ([]byte, error) {
	return nil, errors.New("no supported operating-system credential store")
}
