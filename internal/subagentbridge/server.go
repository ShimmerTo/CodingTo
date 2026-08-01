package subagentbridge

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"codingto/internal/piagent"
)

const (
	maxProtocolMessage = 8 * 1024 * 1024
	runTimeout         = 10 * time.Minute
)

// uiResponseTimeout bounds how long a subagent's interactive extension UI
// request (select/confirm/input/editor) may wait for the frontend to confirm it
// rendered the dialog (see AckSubagentUI, mirroring the parent agent's
// extension_ui_ack). If the frontend never renders the dialog (event lost,
// dialog render failed, frontend gone) the request is auto-cancelled so the
// subagent continues instead of wedging for the entire runTimeout. Once the
// frontend acknowledges rendering or answers directly, the user may take as
// long as they need; only the run context (abort / runTimeout) still applies.
// Kept as a package variable so tests can shorten it.
var uiResponseTimeout = 60 * time.Second

// errUIResponseTimeout reports that an interactive UI request was not
// acknowledged by the frontend within uiResponseTimeout. The caller answers the
// request with a cancellation so the subagent can proceed.
var errUIResponseTimeout = errors.New("subagent UI response timed out")

var (
	runIDPattern  = regexp.MustCompile(`^run-[A-Za-z0-9_-]{8,96}$`)
	nodeIDPattern = regexp.MustCompile(`^turn-\d+$`)
)

type Server struct {
	snapshot Snapshot
	input    io.Reader
	output   io.Writer
	writeMu  sync.Mutex
	activeMu sync.Mutex
	active   map[string]context.CancelFunc
	requests map[string]context.CancelFunc
	sem      chan struct{}
	wg       sync.WaitGroup
}

func NewServer(snapshot Snapshot, input io.Reader, output io.Writer) *Server {
	return &Server{
		snapshot: snapshot, input: input, output: output,
		active: map[string]context.CancelFunc{}, requests: map[string]context.CancelFunc{},
		sem: make(chan struct{}, 1),
	}
}

func (s *Server) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(s.input)
	scanner.Buffer(make([]byte, 64*1024), maxProtocolMessage)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var request Request
		if err := json.Unmarshal(line, &request); err != nil {
			s.writeResponse(Response{
				Version: ProtocolVersion, OK: false,
				Error: &ResponseError{Code: "bad_request", Message: "invalid JSON request"},
			})
			continue
		}
		if request.Version != ProtocolVersion || request.ID == "" {
			s.writeResponse(Response{
				Version: ProtocolVersion, ID: request.ID, OK: false,
				Error: &ResponseError{Code: "bad_request", Message: "invalid protocol version or request id"},
			})
			continue
		}
		if request.Action == "cancel" {
			s.cancelRequest(request)
			continue
		}
		requestCtx, cancel := context.WithCancel(ctx)
		s.activeMu.Lock()
		if _, exists := s.requests[request.ID]; exists {
			s.activeMu.Unlock()
			cancel()
			s.writeError(request.ID, "bad_request", "request id is already active")
			continue
		}
		s.requests[request.ID] = cancel
		s.activeMu.Unlock()
		s.wg.Add(1)
		go s.handle(requestCtx, request, cancel)
	}
	s.activeMu.Lock()
	for _, cancel := range s.requests {
		cancel()
	}
	for _, cancel := range s.active {
		cancel()
	}
	s.activeMu.Unlock()
	s.wg.Wait()
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read subagent request: %w", err)
	}
	return nil
}

func (s *Server) handle(ctx context.Context, request Request, cancel context.CancelFunc) {
	defer s.wg.Done()
	defer cancel()
	defer func() {
		s.activeMu.Lock()
		delete(s.requests, request.ID)
		s.activeMu.Unlock()
	}()

	switch request.Action {
	case "list":
		items := make([]map[string]string, 0, len(s.snapshot.Agents))
		for _, agent := range s.snapshot.Agents {
			items = append(items, map[string]string{
				"key": agent.Key, "name": agent.Name, "description": agent.Description,
				"provider": agent.Provider, "model": agent.Model,
			})
		}
		s.writeResponse(Response{Version: ProtocolVersion, ID: request.ID, OK: true, Result: items})
	case "run":
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		case <-ctx.Done():
			s.writeError(request.ID, "canceled", "subagent request canceled")
			return
		}
		var params RunParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			s.writeError(request.ID, "bad_request", "invalid run parameters")
			return
		}
		result, err := s.run(ctx, params)
		if err != nil && result.RunID == "" {
			s.writeError(request.ID, "run_failed", err.Error())
			return
		}
		s.writeResponse(Response{Version: ProtocolVersion, ID: request.ID, OK: true, Result: result})
	case "status":
		var params struct {
			RunID string `json:"runId"`
		}
		if json.Unmarshal(request.Params, &params) != nil || !runIDPattern.MatchString(params.RunID) {
			s.writeError(request.ID, "bad_request", "invalid runId")
			return
		}
		record, err := ReadRunRecord(filepath.Join(s.snapshot.SessionDir, "subagents", params.RunID, "run.json"))
		if err != nil {
			s.writeError(request.ID, "not_found", "subagent run not found")
			return
		}
		s.writeResponse(Response{Version: ProtocolVersion, ID: request.ID, OK: true, Result: record})
	case "abort":
		var params struct {
			RunID string `json:"runId"`
		}
		if json.Unmarshal(request.Params, &params) != nil || !runIDPattern.MatchString(params.RunID) {
			s.writeError(request.ID, "bad_request", "invalid runId")
			return
		}
		s.activeMu.Lock()
		runCancel := s.active[params.RunID]
		s.activeMu.Unlock()
		if runCancel != nil {
			runCancel()
		}
		s.writeResponse(Response{
			Version: ProtocolVersion, ID: request.ID, OK: true,
			Result: map[string]any{"runId": params.RunID, "aborted": runCancel != nil},
		})
	default:
		s.writeError(request.ID, "unsupported_action", "unsupported subagent action")
	}
}

func (s *Server) cancelRequest(request Request) {
	var params struct {
		RequestID string `json:"requestId"`
	}
	if json.Unmarshal(request.Params, &params) != nil || params.RequestID == "" {
		s.writeError(request.ID, "bad_request", "cancel requires requestId")
		return
	}
	s.activeMu.Lock()
	cancel := s.requests[params.RequestID]
	s.activeMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.writeResponse(Response{
		Version: ProtocolVersion, ID: request.ID, OK: true,
		Result: map[string]any{"requestId": params.RequestID, "canceled": cancel != nil},
	})
}

func (s *Server) run(parent context.Context, params RunParams) (RunResult, error) {
	params.Key = strings.TrimSpace(params.Key)
	params.Task = strings.TrimSpace(params.Task)
	if params.Key == "" || params.Task == "" || !runIDPattern.MatchString(params.RunID) || !nodeIDPattern.MatchString(params.ParentNodeID) {
		return RunResult{}, errors.New("key, task, runId and parentNodeId are required")
	}
	agent, ok := s.snapshot.Agent(params.Key)
	if !ok {
		return RunResult{}, fmt.Errorf("subagent is not authorized: %s", params.Key)
	}
	if agent.Provider == "" || agent.Model == "" {
		return RunResult{}, fmt.Errorf("subagent %s has no default model", params.Key)
	}
	if agent.ConfigError != "" {
		return RunResult{}, fmt.Errorf("subagent %s configuration is invalid: %s", params.Key, agent.ConfigError)
	}
	runDir, err := containedRunDir(s.snapshot.SessionDir, params.RunID)
	if err != nil {
		return RunResult{}, err
	}
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return RunResult{}, err
	}
	nodeID := fmt.Sprintf("turn-%d", time.Now().UnixNano())
	startedAt := time.Now().UnixMilli()
	record := RunRecord{
		Version: 1, RunID: params.RunID, AgentKey: agent.Key, AgentName: agent.Name,
		ParentNodeID: params.ParentNodeID, ToolCallID: params.ToolCallID,
		ChildNodeIDs: []string{nodeID}, Status: "running", Task: params.Task,
		StartedAt: startedAt, Files: []RunFile{},
	}
	if err := writeRunRecord(runDir, record); err != nil {
		return RunResult{}, err
	}
	if err := beginChildNode(runDir, nodeID, s.snapshot.WorkDir, params.Task, startedAt); err != nil {
		return RunResult{}, err
	}

	runCtx, runCancel := context.WithTimeout(parent, runTimeout)
	s.activeMu.Lock()
	s.active[params.RunID] = runCancel
	s.activeMu.Unlock()
	defer func() {
		runCancel()
		s.activeMu.Lock()
		delete(s.active, params.RunID)
		s.activeMu.Unlock()
	}()

	if err := piagent.MaterializeSystemExtensions(agent.DataDir); err != nil {
		return s.finishFailed(runDir, record, "failed", err), err
	}
	if err := piagent.MaterializeBuiltinTools(agent.DataDir, agent.Builtin); err != nil {
		return s.finishFailed(runDir, record, "failed", err), err
	}
	excluded := []string{"codingto_subagent"}
	for _, key := range []string{"read", "bash", "edit", "write"} {
		if enabled, configured := agent.PiTools[key]; configured && !enabled {
			excluded = append(excluded, key)
		}
	}
	extra := []string{"--exclude-tools", strings.Join(excluded, ",")}
	env := cloneEnv(agent.Env)
	env["PI_CODING_AGENT_DIR"] = agent.DataDir
	env["CODINGTO_SESSION_DIR"] = runDir
	env["CODINGTO_WORK_DIR"] = s.snapshot.WorkDir
	env["CODINGTO_MODEL_INPUT_MODALITIES"] = strings.Join(agent.Input, ",")
	delete(env, "CODINGTO_SUBAGENT_KEYS")
	delete(env, "CODINGTO_SUBAGENT_BRIDGE_BIN")
	delete(env, "CODINGTO_SUBAGENT_CONFIG")

	adapter := piagent.NewAdapter()
	if err := adapter.Start(runCtx, piagent.StartConfig{
		WorkDir: s.snapshot.WorkDir, SessionDir: runDir,
		Provider: agent.Provider, Model: agent.Model,
		SessionID: "codingto-subagent-" + params.RunID,
		ExtraArgs: extra, Env: env,
	}); err != nil {
		return s.finishFailed(runDir, record, "failed", err), err
	}
	defer adapter.Stop()
	if agent.ThinkingLevel != "" {
		raw, _ := json.Marshal(map[string]string{"type": "set_thinking_level", "level": agent.ThinkingLevel})
		if err := adapter.SendCommand(raw); err != nil {
			return s.finishFailed(runDir, record, "failed", err), err
		}
	}
	rawPrompt, _ := json.Marshal(map[string]any{"type": "prompt", "message": params.Task})
	if err := adapter.SendCommand(rawPrompt); err != nil {
		return s.finishFailed(runDir, record, "failed", err), err
	}

	var text strings.Builder
	status := "completed"
	errorText := ""
	completed := false
	// Reliable abort path independent of the parent agent: CodingTo writes a
	// .abort marker into the run directory (AbortSubagent / parent AbortPrompt)
	// and this loop polls it, so a wedged parent Pi process can never prevent a
	// subagent from being stopped.
	abortTicker := time.NewTicker(500 * time.Millisecond)
	defer abortTicker.Stop()
	for !completed {
		select {
		case <-runCtx.Done():
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				status, errorText = "timeout", "subagent run timed out"
			} else {
				status, errorText = "aborted", "subagent run aborted"
			}
			completed = true
		case <-abortTicker.C:
			if abortRequested(runDir) {
				status, errorText = "aborted", "subagent run aborted"
				completed = true
			}
		case event, open := <-adapter.Events():
			if !open {
				status = "failed"
				if err := adapter.ExitError(); err != nil {
					errorText = err.Error()
				} else {
					errorText = "subagent process exited before completion"
				}
				completed = true
				break
			}
			recordedAt := time.Now().UnixMilli()
			notified := false
			var payload map[string]any
			if json.Unmarshal(event.Raw, &payload) == nil {
				payload["_recordedAt"] = recordedAt
				payload["changeNodeId"] = nodeID
				payload["runId"] = params.RunID
				payload["agentKey"] = agent.Key
				if persistRunEventForUI(payload) {
					_ = appendEvent(runDir, payload)
				}
				collectText(&text, payload)
				eventType, _ := payload["type"].(string)
				if eventType == "extension_ui_request" && isInteractiveUIRequest(payload) {
					s.writeNotification(Notification{
						Version: ProtocolVersion, Type: "event", RunID: params.RunID,
						AgentKey: agent.Key, Event: event.Raw,
					})
					notified = true
					response, waitErr := awaitUIResponse(runCtx, runDir, stringValue(payload["id"]))
					if waitErr != nil {
						requestID := stringValue(payload["id"])
						if errors.Is(waitErr, errUIResponseTimeout) {
							// The frontend never confirmed it rendered the dialog (event
							// lost, render failed, or frontend gone). Answer it with a
							// cancellation so the subagent unblocks and can continue
							// instead of being wedged until runTimeout. The frontend
							// clears the stale dialog via the subagent_ui_response
							// notification below.
							responseEvent := map[string]any{
								"type": "subagent_ui_response", "id": requestID, "cancelled": true,
								"_recordedAt": time.Now().UnixMilli(), "changeNodeId": nodeID,
								"runId": params.RunID, "agentKey": agent.Key,
							}
							_ = appendEvent(runDir, responseEvent)
							responseRaw, _ := json.Marshal(responseEvent)
							s.writeNotification(Notification{
								Version: ProtocolVersion, Type: "event", RunID: params.RunID,
								AgentKey: agent.Key, Event: responseRaw,
							})
							command := map[string]any{"type": "extension_ui_response", "id": requestID, "cancelled": true}
							commandRaw, _ := json.Marshal(command)
							if err := adapter.SendCommand(commandRaw); err != nil {
								status, errorText = "failed", err.Error()
								completed = true
								break
							}
							log.Printf("[subagent %s] interactive UI request %s timed out; auto-cancelled", params.RunID, requestID)
							continue
						}
						// The run was aborted or timed out while the frontend had the
						// dialog open. Always record the final status so run.json never
						// stays "running" after the run has stopped.
						if errors.Is(waitErr, context.Canceled) {
							status, errorText = "aborted", "subagent run aborted"
						} else if errors.Is(waitErr, context.DeadlineExceeded) {
							status, errorText = "timeout", "subagent run timed out"
						} else {
							status, errorText = "failed", waitErr.Error()
						}
						completed = true
						break
					}
					responseEvent := map[string]any{
						"type": "subagent_ui_response", "id": response["id"],
						"_recordedAt": time.Now().UnixMilli(), "changeNodeId": nodeID,
						"runId": params.RunID, "agentKey": agent.Key,
					}
					_ = appendEvent(runDir, responseEvent)
					responseRaw, _ := json.Marshal(responseEvent)
					s.writeNotification(Notification{
						Version: ProtocolVersion, Type: "event", RunID: params.RunID,
						AgentKey: agent.Key, Event: responseRaw,
					})
					command := map[string]any{"type": "extension_ui_response"}
					for key, value := range response {
						command[key] = value
					}
					commandRaw, _ := json.Marshal(command)
					if err := adapter.SendCommand(commandRaw); err != nil {
						status, errorText = "failed", err.Error()
						completed = true
						break
					}
				}
				if eventType == "agent_end" {
					willRetry, _ := payload["willRetry"].(bool)
					if !willRetry {
						if message := eventError(payload); message != "" {
							status, errorText = "failed", message
						}
						completed = true
					}
				}
			}
			if !notified {
				s.writeNotification(Notification{
					Version: ProtocolVersion, Type: "event", RunID: params.RunID,
					AgentKey: agent.Key, Event: event.Raw,
				})
			}
		}
	}

	endedAt := time.Now().UnixMilli()
	_ = finishChildNode(runDir, nodeID, status, endedAt)
	// Clear the abort marker so a completed run's transcript directory never
	// carries a stale marker that could abort a future run.
	_ = os.Remove(filepath.Join(runDir, ".abort"))
	files := collectRunFiles(runDir, s.snapshot.WorkDir)
	record.Status, record.Text, record.Error = status, strings.TrimSpace(text.String()), errorText
	record.EndedAt, record.Files = endedAt, files
	_ = writeRunRecord(runDir, record)
	result := RunResult{
		RunID: params.RunID, AgentKey: agent.Key, ParentNodeID: params.ParentNodeID,
		Status: status, Text: record.Text, Files: files, Error: errorText,
		Transcript: runDir,
	}
	return result, nil
}

func isInteractiveUIRequest(event map[string]any) bool {
	switch stringValue(event["method"]) {
	case "select", "confirm", "input", "editor":
		return stringValue(event["id"]) != ""
	default:
		return false
	}
}

// abortRequested reports whether CodingTo has dropped a .abort marker into the
// run directory to request a reliable, parent-independent cancellation.
func abortRequested(runDir string) bool {
	_, err := os.Stat(filepath.Join(runDir, ".abort"))
	return err == nil
}

// persistRunEventForUI reports whether an event should be appended to the
// run's codingto_events.jsonl. Run history is rebuilt from Pi's own session
// file ({ts}_codingto-subagent-{runId}.jsonl), so the events log only keeps
// interactive UI requests/responses that restore pending dialog state in the
// details view. Streaming events (message_update deltas, tool execution
// updates, ...) carry cumulative snapshots and would otherwise balloon the
// file to gigabytes.
func persistRunEventForUI(event map[string]any) bool {
	switch stringValue(event["type"]) {
	case "extension_ui_request", "subagent_ui_response":
		return true
	default:
		return false
	}
}

func stringValue(value any) string {
	switch item := value.(type) {
	case string:
		return item
	case json.Number:
		return item.String()
	default:
		return ""
	}
}

func awaitUIResponse(ctx context.Context, runDir, requestID string) (map[string]any, error) {
	if requestID == "" || len(requestID) > 512 {
		return nil, errors.New("subagent emitted an invalid UI request id")
	}
	sum := sha256.Sum256([]byte(requestID))
	ackPath := filepath.Join(runDir, ".ui-acks", fmt.Sprintf("%x.json", sum))
	responsePath := filepath.Join(runDir, ".ui-responses", fmt.Sprintf("%x.json", sum))
	// The frontend answers interactive UI requests by dropping a response file
	// into the run's response mailbox, and confirms it rendered the dialog by
	// dropping an ack file into the ack mailbox. The UI timeout below only
	// bounds the ack window, mirroring the parent agent's watchdog that
	// extension_ui_ack disarms: a dialog the frontend never renders must not
	// wedge the subagent for the whole run, but once the dialog is confirmed the
	// user may answer at their own pace, bounded only by the run context.
	ackTimeout := time.NewTimer(uiResponseTimeout)
	defer ackTimeout.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	acknowledged := false
	for {
		raw, err := os.ReadFile(responsePath)
		if err == nil {
			var response map[string]any
			if json.Unmarshal(raw, &response) != nil || stringValue(response["id"]) != requestID {
				return nil, errors.New("invalid subagent UI response")
			}
			_ = os.Remove(responsePath)
			return response, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if !acknowledged {
			// Phase 1: wait for the frontend to acknowledge rendering (or answer
			// directly). Once acknowledged, uiResponseTimeout no longer applies.
			if _, statErr := os.Stat(ackPath); statErr == nil {
				acknowledged = true
				continue
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return nil, statErr
			}
		}
		// The run loop is blocked in this function while the dialog is open, so
		// its own abort ticker cannot run. Poll the abort marker here instead:
		// the user clicking stop must stop the subagent immediately, not after
		// runTimeout (10 minutes). The run loop records context.Canceled as
		// "aborted".
		if abortRequested(runDir) {
			return nil, context.Canceled
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ackTimeout.C:
			// If the timer fired just as the ack arrived, the ack check above
			// takes precedence; only cancel when the dialog was never confirmed.
			if !acknowledged {
				// Double-check: the ack may have landed in the tiny gap between
				// the stat above and the timer firing.
				if _, statErr := os.Stat(ackPath); statErr == nil {
					acknowledged = true
					continue
				}
				return nil, errUIResponseTimeout
			}
		case <-ticker.C:
		}
	}
}

func (s *Server) finishFailed(runDir string, record RunRecord, status string, cause error) RunResult {
	endedAt := time.Now().UnixMilli()
	if len(record.ChildNodeIDs) > 0 {
		_ = finishChildNode(runDir, record.ChildNodeIDs[0], status, endedAt)
	}
	record.Status, record.Error, record.EndedAt = status, cause.Error(), endedAt
	record.Files = collectRunFiles(runDir, s.snapshot.WorkDir)
	_ = writeRunRecord(runDir, record)
	return RunResult{
		RunID: record.RunID, AgentKey: record.AgentKey, ParentNodeID: record.ParentNodeID,
		Status: status, Files: record.Files, Error: cause.Error(), Transcript: runDir,
	}
}

func (s *Server) writeError(id, code, message string) {
	s.writeResponse(Response{
		Version: ProtocolVersion, ID: id, OK: false,
		Error: &ResponseError{Code: code, Message: message},
	})
}

func (s *Server) writeResponse(response Response)      { s.writeJSON(response) }
func (s *Server) writeNotification(value Notification) { s.writeJSON(value) }

func (s *Server) writeJSON(value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	raw = append(raw, '\n')
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = s.output.Write(raw)
}

func containedRunDir(sessionDir, runID string) (string, error) {
	root, err := filepath.Abs(filepath.Join(sessionDir, "subagents"))
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(root, runID))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", errors.New("subagent run path escapes session directory")
	}
	return target, nil
}

func cloneEnv(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func beginChildNode(runDir, nodeID, workDir, task string, startedAt int64) error {
	nodeDir := filepath.Join(runDir, "changes", "nodes", nodeID)
	if err := os.MkdirAll(nodeDir, 0o700); err != nil {
		return err
	}
	manifest := map[string]any{
		"version": 2, "id": nodeID, "root": workDir, "prompt": task,
		"startedAt": startedAt, "status": "running", "files": map[string]any{},
	}
	if err := writeJSONAtomic(filepath.Join(nodeDir, "manifest.json"), manifest); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runDir, ".active-change-node"), []byte(nodeID), 0o600)
}

func finishChildNode(runDir, nodeID, status string, endedAt int64) error {
	path := filepath.Join(runDir, "changes", "nodes", nodeID, "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	manifest["status"], manifest["endedAt"] = status, endedAt
	return writeJSONAtomic(path, manifest)
}

func writeRunRecord(runDir string, record RunRecord) error {
	return writeJSONAtomic(filepath.Join(runDir, "run.json"), record)
}

func writeJSONAtomic(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp := fmt.Sprintf("%s.tmp-%d", path, time.Now().UnixNano())
	if err := os.WriteFile(temp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		// Windows does not replace an existing destination with Rename. Run
		// records are private, recoverable metadata, so replace the exact file
		// and immediately retry the atomic move.
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			_ = os.Remove(temp)
			return err
		}
		if retryErr := os.Rename(temp, path); retryErr != nil {
			_ = os.Remove(temp)
			return retryErr
		}
	}
	return nil
}

func appendEvent(runDir string, event any) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return appendRawEvent(runDir, raw)
}

func appendRawEvent(runDir string, raw []byte) error {
	file, err := os.OpenFile(filepath.Join(runDir, "codingto_events.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(append([]byte(nil), raw...), '\n'))
	return err
}

func collectText(output *strings.Builder, event map[string]any) {
	eventType, _ := event["type"].(string)
	if eventType == "message_update" {
		update, _ := event["assistantMessageEvent"].(map[string]any)
		if update["type"] == "text_delta" {
			if delta, ok := update["delta"].(string); ok {
				output.WriteString(delta)
			}
		}
	}
	if output.Len() != 0 {
		return
	}
	if eventType == "message_end" {
		if text := assistantText(event["message"]); text != "" {
			output.WriteString(text)
		}
	}
	if eventType == "agent_end" {
		messages, _ := event["messages"].([]any)
		for index := len(messages) - 1; index >= 0; index-- {
			if text := assistantText(messages[index]); text != "" {
				output.WriteString(text)
				break
			}
		}
	}
}

func assistantText(value any) string {
	message, _ := value.(map[string]any)
	if role, _ := message["role"].(string); role != "assistant" {
		return ""
	}
	if text, ok := message["content"].(string); ok {
		return text
	}
	blocks, _ := message["content"].([]any)
	var result strings.Builder
	for _, value := range blocks {
		block, _ := value.(map[string]any)
		blockType, _ := block["type"].(string)
		if blockType != "text" {
			continue
		}
		if text, ok := block["text"].(string); ok {
			result.WriteString(text)
		}
	}
	return result.String()
}

func eventError(event map[string]any) string {
	if message, ok := event["errorMessage"].(string); ok && message != "" {
		return message
	}
	messages, _ := event["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if text, ok := message["errorMessage"].(string); ok && text != "" {
			return text
		}
	}
	return ""
}

func collectRunFiles(runDir, workDir string) []RunFile {
	files := map[string]RunFile{}
	nodesRoot := filepath.Join(runDir, "changes", "nodes")
	nodeEntries, _ := os.ReadDir(nodesRoot)
	for _, node := range nodeEntries {
		if !node.IsDir() {
			continue
		}
		pathsRoot := filepath.Join(nodesRoot, node.Name(), "captures", "paths")
		pathEntries, _ := os.ReadDir(pathsRoot)
		for _, entry := range pathEntries {
			if !entry.IsDir() {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(pathsRoot, entry.Name(), "meta.json"))
			if err != nil {
				continue
			}
			var meta struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(raw, &meta) != nil || meta.Path == "" {
				continue
			}
			absolute := filepath.Join(workDir, filepath.FromSlash(meta.Path))
			info, statErr := os.Stat(absolute)
			change := "modified"
			bytes := int64(0)
			if statErr == nil {
				bytes = info.Size()
				beforeRaw, beforeErr := os.ReadFile(filepath.Join(pathsRoot, entry.Name(), "before.json"))
				if beforeErr == nil {
					var before struct {
						Snapshot struct {
							Exists bool `json:"exists"`
						} `json:"snapshot"`
					}
					if json.Unmarshal(beforeRaw, &before) == nil && !before.Snapshot.Exists {
						change = "added"
					}
				}
			} else if os.IsNotExist(statErr) {
				change = "deleted"
			}
			files["file:"+absolute] = RunFile{Path: absolute, Change: change, Kind: fileKind(absolute), Bytes: bytes}
		}
	}
	for _, kind := range []string{"browser", "document"} {
		root := filepath.Join(runDir, "artifacts", kind)
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry == nil || entry.IsDir() {
				return nil
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				return nil
			}
			files["artifact:"+path] = RunFile{
				Path: path, Change: "artifact", Kind: fileKind(path), Bytes: info.Size(),
			}
			return nil
		})
	}
	result := make([]RunFile, 0, len(files))
	for _, file := range files {
		result = append(result, file)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}

func fileKind(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tiff":
		return "image"
	case ".pdf":
		return "pdf"
	case ".md", ".markdown":
		return "markdown"
	default:
		return "file"
	}
}
