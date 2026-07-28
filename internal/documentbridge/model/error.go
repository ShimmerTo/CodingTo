package model

import "fmt"

type BridgeError struct {
	Code      string
	Message   string
	Retryable bool
	Err       error
}

func (e *BridgeError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *BridgeError) Unwrap() error { return e.Err }

func Error(code, message string, err error) error {
	return &BridgeError{Code: code, Message: message, Err: err}
}
