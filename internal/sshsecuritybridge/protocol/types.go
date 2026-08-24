package protocol

import (
	"encoding/json"
	"fmt"
)

// Version is the SSH bridge JSONL protocol version.
const Version = 1

// Request is one JSONL bridge request.
type Request struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Action  string          `json:"action"`
	Params  json.RawMessage `json:"params"`
}

// Response is one JSONL bridge response.
type Response struct {
	Version int            `json:"version"`
	ID      string         `json:"id"`
	OK      bool           `json:"ok"`
	Result  any            `json:"result,omitempty"`
	Error   *ResponseError `json:"error,omitempty"`
}

// ResponseError is the stable error shape returned to the Agent extension.
type ResponseError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// BridgeError is a user-safe business error.
type BridgeError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *BridgeError) Error() string { return e.Code + ": " + e.Message }

// Errorf creates a user-safe bridge error.
func Errorf(code, format string, args ...any) *BridgeError {
	return &BridgeError{Code: code, Message: fmt.Sprintf(format, args...)}
}
