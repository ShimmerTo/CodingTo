package protocol

import (
	"encoding/json"
	"fmt"
)

const Version = 1

type Request struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Action  string          `json:"action"`
	Params  json.RawMessage `json:"params"`
}

type Response struct {
	Version int            `json:"version"`
	ID      string         `json:"id"`
	OK      bool           `json:"ok"`
	Result  any            `json:"result,omitempty"`
	Error   *ResponseError `json:"error,omitempty"`
}

type ResponseError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// BridgeError 是业务错误；server 转换为协议错误响应。
type BridgeError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *BridgeError) Error() string {
	return e.Code + ": " + e.Message
}

// Errorf 构造 BridgeError。
func Errorf(code, format string, args ...any) *BridgeError {
	return &BridgeError{Code: code, Message: fmt.Sprintf(format, args...)}
}
