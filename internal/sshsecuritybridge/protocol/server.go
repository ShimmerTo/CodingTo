package protocol

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

const maxMessageBytes = 1024 * 1024

// Handler dispatches bridge actions.
type Handler interface {
	Handle(ctx context.Context, requestID, action string, params json.RawMessage) (any, error)
}

// Server runs the cancellable, single-execution JSONL protocol.
type Server struct {
	handler Handler
	input   io.Reader
	output  io.Writer
	writeMu sync.Mutex
	mu      sync.Mutex
	active  map[string]context.CancelFunc
	heavy   chan struct{}
	wg      sync.WaitGroup
}

// NewServer creates an SSH bridge protocol server.
func NewServer(handler Handler, input io.Reader, output io.Writer) *Server {
	return &Server{handler: handler, input: input, output: output, active: map[string]context.CancelFunc{}, heavy: make(chan struct{}, 1)}
}

// Run reads requests until stdin closes, then cancels outstanding work.
func (s *Server) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(s.input)
	scanner.Buffer(make([]byte, 64*1024), maxMessageBytes)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var request Request
		if err := json.Unmarshal(line, &request); err != nil {
			s.write(Response{Version: Version, Error: &ResponseError{Code: "bad_request", Message: "无效 JSON 请求"}})
			continue
		}
		if request.Action == "cancel" {
			s.cancel(request)
			continue
		}
		if request.Version != Version || request.ID == "" {
			s.write(Response{Version: Version, ID: request.ID, Error: &ResponseError{Code: "bad_request", Message: "协议版本或 request id 不合法"}})
			continue
		}
		requestCtx, cancel := context.WithCancel(ctx)
		s.mu.Lock()
		if _, exists := s.active[request.ID]; exists {
			s.mu.Unlock()
			cancel()
			s.write(Response{Version: Version, ID: request.ID, Error: &ResponseError{Code: "bad_request", Message: "request id 正在使用"}})
			continue
		}
		s.active[request.ID] = cancel
		s.mu.Unlock()
		s.wg.Add(1)
		go s.handle(requestCtx, request, cancel)
	}
	s.mu.Lock()
	for _, cancel := range s.active {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read JSONL request: %w", err)
	}
	return nil
}

func (s *Server) handle(ctx context.Context, request Request, cancel context.CancelFunc) {
	defer s.wg.Done()
	defer cancel()
	defer func() {
		s.mu.Lock()
		delete(s.active, request.ID)
		s.mu.Unlock()
		if recover() != nil {
			s.write(Response{Version: Version, ID: request.ID, Error: &ResponseError{Code: "internal_error", Message: "请求处理发生内部错误"}})
		}
	}()
	select {
	case s.heavy <- struct{}{}:
		defer func() { <-s.heavy }()
	case <-ctx.Done():
		s.write(errorResponse(request.ID, ctx.Err()))
		return
	}
	result, err := s.handler.Handle(ctx, request.ID, request.Action, request.Params)
	if err != nil {
		s.write(errorResponse(request.ID, err))
		return
	}
	s.write(Response{Version: Version, ID: request.ID, OK: true, Result: result})
}

func (s *Server) cancel(request Request) {
	var params struct {
		RequestID string `json:"requestId"`
	}
	if request.Version != Version || json.Unmarshal(request.Params, &params) != nil || params.RequestID == "" {
		s.write(Response{Version: Version, ID: request.ID, Error: &ResponseError{Code: "bad_request", Message: "cancel 参数不合法"}})
		return
	}
	s.mu.Lock()
	cancel := s.active[params.RequestID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.write(Response{Version: Version, ID: request.ID, OK: true, Result: map[string]any{"requestId": params.RequestID, "canceled": cancel != nil}})
}

func (s *Server) write(response Response) {
	raw, err := json.Marshal(response)
	if err != nil {
		return
	}
	if len(raw) > maxMessageBytes {
		raw, _ = json.Marshal(Response{Version: Version, ID: response.ID, Error: &ResponseError{Code: "resource_limit", Message: "响应超过 1MB 协议限制"}})
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = s.output.Write(append(raw, '\n'))
}

func errorResponse(id string, err error) Response {
	var bridgeError *BridgeError
	if errors.As(err, &bridgeError) {
		return Response{Version: Version, ID: id, Error: &ResponseError{Code: bridgeError.Code, Message: bridgeError.Message, Retryable: bridgeError.Retryable}}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Response{Version: Version, ID: id, Error: &ResponseError{Code: "canceled", Message: "请求已取消或超时"}}
	}
	return Response{Version: Version, ID: id, Error: &ResponseError{Code: "internal_error", Message: "内部错误"}}
}
