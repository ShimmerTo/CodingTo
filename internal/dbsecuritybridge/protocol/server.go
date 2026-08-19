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

// Handler 是动作分发器，由 bridge service 实现。
type Handler interface {
	Handle(ctx context.Context, requestID, action string, params json.RawMessage) (any, error)
}

// Server 是 JSONL stdin/stdout 服务，结构对齐 document-bridge：
// 写锁保证响应按行完整输出；active map 支持 cancel；heavy 通道限制
// 并发执行数为 1（DB 操作串行，避免连接池压力）。
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

func NewServer(handler Handler, input io.Reader, output io.Writer) *Server {
	return &Server{
		handler: handler, input: input, output: output,
		active: map[string]context.CancelFunc{}, heavy: make(chan struct{}, 1),
	}
}

func (s *Server) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(s.input)
	scanner.Buffer(make([]byte, 64*1024), maxMessageBytes)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var request Request
		if err := json.Unmarshal(line, &request); err != nil {
			s.write(Response{
				Version: Version, ID: "", OK: false,
				Error: &ResponseError{Code: "bad_request", Message: "无效 JSON 请求：" + err.Error()},
			})
			continue
		}
		if request.Action == "cancel" {
			s.cancel(request)
			continue
		}
		if request.Version != Version {
			s.write(Response{
				Version: Version, ID: request.ID, OK: false,
				Error: &ResponseError{Code: "unsupported_protocol_version", Message: fmt.Sprintf("仅支持协议版本 %d", Version)},
			})
			continue
		}
		if request.ID == "" {
			s.write(Response{
				Version: Version, ID: "", OK: false,
				Error: &ResponseError{Code: "bad_request", Message: "request id 不能为空"},
			})
			continue
		}
		requestCtx, cancel := context.WithCancel(ctx)
		s.mu.Lock()
		if _, exists := s.active[request.ID]; exists {
			s.mu.Unlock()
			cancel()
			s.write(Response{
				Version: Version, ID: request.ID, OK: false,
				Error: &ResponseError{Code: "bad_request", Message: "request id 正在使用"},
			})
			continue
		}
		s.active[request.ID] = cancel
		s.mu.Unlock()
		s.wg.Add(1)
		go s.handle(requestCtx, request, cancel)
	}
	scanErr := scanner.Err()
	s.mu.Lock()
	for _, cancel := range s.active {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	if scanErr != nil {
		return fmt.Errorf("read JSONL request: %w", scanErr)
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
		if recovered := recover(); recovered != nil {
			s.write(Response{
				Version: Version, ID: request.ID, OK: false,
				Error: &ResponseError{Code: "internal_error", Message: "请求处理发生内部错误"},
			})
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
	if request.Version != Version {
		s.write(Response{
			Version: Version, ID: request.ID, OK: false,
			Error: &ResponseError{Code: "unsupported_protocol_version", Message: fmt.Sprintf("仅支持协议版本 %d", Version)},
		})
		return
	}
	var params struct {
		RequestID string `json:"requestId"`
	}
	if json.Unmarshal(request.Params, &params) != nil || params.RequestID == "" {
		s.write(Response{
			Version: Version, ID: request.ID, OK: false,
			Error: &ResponseError{Code: "bad_request", Message: "cancel 缺少 requestId"},
		})
		return
	}
	s.mu.Lock()
	cancel := s.active[params.RequestID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.write(Response{
		Version: Version, ID: request.ID, OK: true,
		Result: map[string]any{"requestId": params.RequestID, "canceled": cancel != nil},
	})
}

func (s *Server) write(response Response) {
	raw, err := json.Marshal(response)
	if err != nil {
		return
	}
	if len(raw) > maxMessageBytes {
		raw, _ = json.Marshal(Response{
			Version: Version, ID: response.ID, OK: false,
			Error: &ResponseError{Code: "resource_limit", Message: "响应超过 1MB 协议限制"},
		})
	}
	raw = append(raw, '\n')
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = s.output.Write(raw)
}

func errorResponse(id string, err error) Response {
	var bridgeError *BridgeError
	if errors.As(err, &bridgeError) {
		return Response{
			Version: Version, ID: id, OK: false,
			Error: &ResponseError{
				Code: bridgeError.Code, Message: bridgeError.Message, Retryable: bridgeError.Retryable,
			},
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Response{
			Version: Version, ID: id, OK: false,
			Error: &ResponseError{Code: "canceled", Message: "请求已取消或超时"},
		}
	}
	return Response{
		Version: Version, ID: id, OK: false,
		Error: &ResponseError{Code: "internal_error", Message: "内部错误"},
	}
}
