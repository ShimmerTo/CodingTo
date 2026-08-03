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
	// A detached run may occupy one of the model slots or wait for one. Keep
	// the waiting portion bounded so a caller cannot accumulate an unbounded
	// number of run directories, goroutines, and cancellation polls.
	maxQueuedRunsMultiplier = 2

	// Widget updates are UI-only snapshots. Keep both individual fields and the
	// complete notification bounded so a detached agent cannot flood the parent
	// session with an unbounded widget payload.
	maxWidgetNotificationBytes = 8 * 1024
	maxWidgetKeyBytes          = 256
	maxWidgetLineBytes         = 1024
	maxWidgetLines             = 200
	widgetFlushInterval        = 80 * time.Millisecond
)

// uiResponseTimeout bounds how long a subagent's interactive extension UI
// request (select/confirm/input/editor) may wait for the frontend to confirm it
// rendered the dialog (see AckSubagentUI, mirroring the parent agent's
// extension_ui_ack). If the frontend never renders the dialog (event lost,
// dialog render failed, frontend gone) the request is auto-cancelled so the
// subagent continues instead of remaining blocked on an invisible dialog. Once the
// frontend acknowledges rendering or answers directly, the user may take as
// long as they need; only cancellation of the run context still applies.
// Kept as a package variable so tests can shorten it.
var uiResponseTimeout = 60 * time.Second

// uiResponseRetryWindow covers the small interval in which a frontend mailbox
// file can be observed after creation but before its JSON bytes are complete.
// A permanently malformed response still fails closed after this bounded
// retry period; id validation remains immediate once JSON is complete.
var uiResponseRetryWindow = 500 * time.Millisecond

// errUIResponseTimeout reports that an interactive UI request was not
// acknowledged by the frontend within uiResponseTimeout. The caller answers the
// request with a cancellation so the subagent can proceed.
var errUIResponseTimeout = errors.New("subagent UI response timed out")

var (
	runIDPattern  = regexp.MustCompile(`^run-[A-Za-z0-9_-]{8,96}$`)
	nodeIDPattern = regexp.MustCompile(`^turn-\d+$`)
	// errQueueFull is deliberately stable: the bridge handler maps it to the
	// queue_full protocol error so callers can retry without treating it as a
	// malformed run or an execution failure.
	errQueueFull = errors.New("subagent run queue is full")
)

type Server struct {
	snapshot      Snapshot
	input         io.Reader
	output        io.Writer
	writeMu       sync.Mutex
	activeMu      sync.Mutex
	materializeMu sync.Mutex
	active        map[string]context.CancelFunc
	requests      map[string]context.CancelFunc
	sem           chan struct{}
	admission     chan struct{}
	queueMu       sync.Mutex
	queueWaiters  int
	wg            sync.WaitGroup
	wedge         wedgeParams
}

func NewServer(snapshot Snapshot, input io.Reader, output io.Writer) *Server {
	snapshot.MaxConcurrency = NormalizeConcurrency(snapshot.MaxConcurrency)
	return &Server{
		snapshot: snapshot, input: input, output: output,
		active: map[string]context.CancelFunc{}, requests: map[string]context.CancelFunc{},
		sem:       make(chan struct{}, snapshot.MaxConcurrency),
		admission: make(chan struct{}, snapshot.MaxConcurrency*(1+maxQueuedRunsMultiplier)),
		wedge:     loadWedgeParams(),
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
		var params RunParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			s.writeError(request.ID, "bad_request", "invalid run parameters")
			return
		}
		result, err := s.run(ctx, params)
		if err != nil && result.RunID == "" {
			if errors.Is(err, errQueueFull) {
				s.writeErrorRetryable(request.ID, "queue_full", err.Error(), true)
			} else {
				s.writeError(request.ID, "run_failed", err.Error())
			}
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
		requested := s.requestAbort(params.RunID)
		s.writeResponse(Response{
			Version: ProtocolVersion, ID: request.ID, OK: true,
			Result: map[string]any{"runId": params.RunID, "aborted": requested, "requested": requested},
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
	if !s.tryAcquireAdmission() {
		return RunResult{}, errQueueFull
	}
	defer s.releaseAdmission()

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
		// run.json was already published as running. Even if the child manifest
		// was never created, persist a terminal run state and clear all markers.
		result := s.finishFailed(runDir, record, "failed", err)
		return result, err
	}

	// Register cancellation before notifying the parent. A parent can respond
	// to subagent_run_started immediately, so the bridge must already have a
	// cancellable active entry when that notification is observed.
	runCtx, runCancel := context.WithCancel(parent)
	s.activeMu.Lock()
	s.active[params.RunID] = runCancel
	s.activeMu.Unlock()
	defer func() {
		runCancel()
		s.activeMu.Lock()
		delete(s.active, params.RunID)
		s.activeMu.Unlock()
	}()

	// A detached parent tool call only waits for this acknowledgement, not for
	// the child model to finish. Emit it after run.json and the child change node
	// exist so the parent node can safely persist the run reference immediately.
	startedEvent, _ := json.Marshal(map[string]any{
		"type": "subagent_run_started", "runId": params.RunID,
		"agentKey": agent.Key, "changeNodeId": nodeID, "_recordedAt": startedAt,
	})
	s.writeNotification(Notification{
		Version: ProtocolVersion, Type: "event", RunID: params.RunID,
		AgentKey: agent.Key, Event: startedEvent,
	})

	// Reserve the expensive model-process slot only after the durable run record
	// and start acknowledgement exist. The admission channel bounds active plus
	// queued detached runs; the queue waiter count makes wait time and pressure
	// observable without adding persistent status files.
	queueStartedAt := time.Now()
	s.queueMu.Lock()
	s.queueWaiters++
	queueLength := s.queueWaiters
	s.queueMu.Unlock()
	waiting := true
	defer func() {
		if waiting {
			s.queueMu.Lock()
			s.queueWaiters--
			s.queueMu.Unlock()
		}
	}()
	if len(s.sem) >= cap(s.sem) {
		log.Printf("[subagent %s] queued: queue length=%d", params.RunID, queueLength)
	}
	queueTicker := time.NewTicker(500 * time.Millisecond)
	defer queueTicker.Stop()
	for {
		if result, stopErr, stopped := s.finishIfAborted(runCtx, runCancel, runDir, record); stopped {
			return result, stopErr
		}
		select {
		case s.sem <- struct{}{}:
			s.queueMu.Lock()
			s.queueWaiters--
			remaining := s.queueWaiters
			s.queueMu.Unlock()
			waiting = false
			if waited := time.Since(queueStartedAt); waited >= 500*time.Millisecond {
				log.Printf("[subagent %s] acquired run slot after %s; queue length=%d", params.RunID, waited.Round(time.Millisecond), remaining)
			}
			defer func() { <-s.sem }()
			goto slotAcquired
		case <-runCtx.Done():
			result := s.finishFailed(runDir, record, "aborted", errors.New("subagent run aborted"))
			return result, runCtx.Err()
		case <-queueTicker.C:
			if abortRequested(runDir) {
				runCancel()
			}
		}
	}

slotAcquired:
	// A marker can arrive after semaphore acquisition while preparation is
	// still pending. Do not materialize or start a model for a cancelled run.
	if result, stopErr, stopped := s.finishIfAborted(runCtx, runCancel, runDir, record); stopped {
		return result, stopErr
	}

	// Runs are concurrent, but materialization writes into the shared data
	// directory of the selected Agent. Keep this short preparation phase
	// serialized so two runs of the same Agent cannot rewrite extensions at the
	// same time; the model processes start and execute in parallel afterwards.
	prepareErr := s.materializeAgent(agent)
	if prepareErr != nil {
		return s.finishFailed(runDir, record, "failed", prepareErr), prepareErr
	}
	if result, stopErr, stopped := s.finishIfAborted(runCtx, runCancel, runDir, record); stopped {
		return result, stopErr
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
	if result, stopErr, stopped := s.finishIfAborted(runCtx, runCancel, runDir, record); stopped {
		return result, stopErr
	}
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
		if result, stopErr, stopped := s.finishIfAborted(runCtx, runCancel, runDir, record); stopped {
			return result, stopErr
		}
		if err := adapter.SendCommand(raw); err != nil {
			return s.finishFailed(runDir, record, "failed", err), err
		}
	}
	rawPrompt, _ := json.Marshal(map[string]any{"type": "prompt", "message": params.Task})
	if result, stopErr, stopped := s.finishIfAborted(runCtx, runCancel, runDir, record); stopped {
		return result, stopErr
	}
	if err := adapter.SendCommand(rawPrompt); err != nil {
		return s.finishFailed(runDir, record, "failed", err), err
	}

	var text strings.Builder
	status := "completed"
	errorText := ""
	completed := false
	// setWidget is a non-blocking extension UI request. It can be emitted for
	// every plan step, so retain only the latest snapshot for each key and flush
	// it at a human-visible cadence. This state belongs to one run and is
	// explicitly flushed/cleared on every terminal path below.
	widgetQueue := newWidgetNotificationQueue()
	widgetTicker := time.NewTicker(widgetFlushInterval)
	defer func() {
		widgetTicker.Stop()
		widgetQueue.clear()
	}()
	flushWidgets := func() {
		widgetQueue.flush(func(event json.RawMessage) {
			if err := appendRawEvent(runDir, event); err != nil {
				log.Printf("[subagent %s] persist widget snapshot: %v", params.RunID, err)
			}
			s.writeNotification(Notification{
				Version: ProtocolVersion, Type: "event", RunID: params.RunID,
				AgentKey: agent.Key, Event: event,
			})
		})
	}
	// Reliable abort path independent of the parent agent: CodingTo writes a
	// .abort marker into the run directory (AbortSubagent / parent AbortPrompt)
	// and this loop polls it, so a wedged parent Pi process can never prevent a
	// subagent from being stopped.
	abortTicker := time.NewTicker(500 * time.Millisecond)
	defer abortTicker.Stop()
	// Silence watchdog: RPC mode streams every AgentSessionEvent (including
	// model thinking/reply chunks) to stdout, so a child silent for
	// SilenceMax has produced no output of any kind and is wedged on a dead
	// model connection. Kill the whole process tree first (the direct child
	// on Windows is a cmd.exe shim with node underneath), then cancel so the
	// run reaches a terminal state and the parent session keeps moving.
	lastEventAt := time.Now()
	wedgeTicker := time.NewTicker(s.wedge.Tick)
	defer wedgeTicker.Stop()
	var wedgeReason string
	for !completed {
		select {
		case <-runCtx.Done():
			if wedgeReason != "" {
				status, errorText = "failed", wedgeReason
			} else {
				status, errorText = "aborted", "subagent run aborted"
			}
			completed = true
		case <-wedgeTicker.C:
			if reason := s.wedge.decide(time.Now(), lastEventAt); reason != "" {
				wedgeReason = reason
				log.Printf("[subagent %s] wedge watchdog triggered: %s", params.RunID, reason)
				adapter.KillTree()
				runCancel()
				status, errorText = "failed", wedgeReason
				completed = true
			}
		case <-abortTicker.C:
			if abortRequested(runDir) {
				status, errorText = "aborted", "subagent run aborted"
				completed = true
			}
		case <-widgetTicker.C:
			flushWidgets()
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
			if result, stopErr, stopped := s.finishIfAborted(runCtx, runCancel, runDir, record); stopped {
				return result, stopErr
			}
			recordedAt := time.Now().UnixMilli()
			notified := false
			var payload map[string]any
			if json.Unmarshal(event.Raw, &payload) == nil {
				payload["_recordedAt"] = recordedAt
				payload["changeNodeId"] = nodeID
				payload["runId"] = params.RunID
				payload["agentKey"] = agent.Key
				collectText(&text, payload)
				eventType, _ := payload["type"].(string)
				// Any event - including model thinking/streaming chunks - is
				// progress and resets the wedge silence window.
				lastEventAt = time.Now()
				if eventType == "extension_ui_request" && stringValue(payload["method"]) == "setWidget" {
					// Invalid/oversized snapshots are intentionally dropped. They are
					// UI-only and must never become a large notification or enter the
					// parent's LLM context.
					if widgetRaw, marshalErr := json.Marshal(payload); marshalErr == nil {
						_ = widgetQueue.enqueue(widgetRaw, payload)
					}
					continue
				}
				// Flush before a different event so the low-frequency snapshot does
				// not appear after the lifecycle event that followed it.
				flushWidgets()
				if persistRunEventForUI(payload) {
					_ = appendEvent(runDir, payload)
				}
				if eventType == "extension_ui_request" && isInteractiveUIRequest(payload) {
					s.writeNotification(Notification{
						Version: ProtocolVersion, Type: "event", RunID: params.RunID,
						AgentKey: agent.Key, Event: event.Raw,
					})
					notified = true
					response, waitErr := awaitUIResponse(runCtx, runDir, stringValue(payload["id"]))
					// Waiting for a rendered dialog is legitimate work. The run loop is
					// synchronous while it waits, so give the child a fresh silence window
					// after the user responds (or the UI wait otherwise returns).
					lastEventAt = time.Now()
					if waitErr != nil {
						requestID := stringValue(payload["id"])
						if errors.Is(waitErr, errUIResponseTimeout) {
							// The frontend never confirmed it rendered the dialog (event
							// lost, render failed, or frontend gone). Answer it with a
							// cancellation so the subagent unblocks and can continue
							// instead of remaining blocked on an invisible dialog. The frontend
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
							if result, stopErr, stopped := s.finishIfAborted(runCtx, runCancel, runDir, record); stopped {
								return result, stopErr
							}
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
					if result, stopErr, stopped := s.finishIfAborted(runCtx, runCancel, runDir, record); stopped {
						return result, stopErr
					}
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
			if !notified && forwardRunEvent(payload) {
				s.writeNotification(Notification{
					Version: ProtocolVersion, Type: "event", RunID: params.RunID,
					AgentKey: agent.Key, Event: event.Raw,
				})
			}
		}
	}

	// Deliver the last coalesced snapshot before publishing the terminal run
	// state, then clear it via the defer above. If the run was cancelled through
	// an early return, the defer still discards the per-run state.
	flushWidgets()

	endedAt := time.Now().UnixMilli()
	if err := finishChildNode(runDir, nodeID, status, endedAt); err != nil {
		log.Printf("[subagent %s] finish child change node: %v", params.RunID, err)
	}
	if err := cleanupRunMarkers(runDir); err != nil {
		log.Printf("[subagent %s] clean run markers: %v", params.RunID, err)
	}
	files := collectRunFiles(runDir, s.snapshot.WorkDir)
	record.Status, record.Text, record.Error = status, strings.TrimSpace(text.String()), errorText
	record.EndedAt, record.Files = endedAt, files
	if err := writeRunRecord(runDir, record); err != nil {
		log.Printf("[subagent %s] persist terminal run record: %v", params.RunID, err)
	}
	result := RunResult{
		RunID: params.RunID, AgentKey: agent.Key, ParentNodeID: params.ParentNodeID,
		Status: status, Text: record.Text, Files: files, Error: errorText,
		Transcript: runDir,
	}
	return result, nil
}

// Detached parent tools no longer consume high-frequency token updates after
// startup. Forward only compact activity events needed by the card/UI; the
// complete conversation remains in the child Pi session and final RunResult.
func forwardRunEvent(event map[string]any) bool {
	switch stringValue(event["type"]) {
	case "agent_start", "tool_execution_start", "tool_execution_end":
		return true
	default:
		return false
	}
}

func (s *Server) materializeAgent(agent AgentConfig) error {
	s.materializeMu.Lock()
	defer s.materializeMu.Unlock()
	if err := piagent.MaterializeSystemExtensions(agent.DataDir); err != nil {
		return err
	}
	return piagent.MaterializeBuiltinTools(agent.DataDir, agent.Builtin)
}

type widgetNotificationQueue struct {
	pending map[string]json.RawMessage
}

func newWidgetNotificationQueue() *widgetNotificationQueue {
	return &widgetNotificationQueue{pending: map[string]json.RawMessage{}}
}

func (q *widgetNotificationQueue) enqueue(raw json.RawMessage, event map[string]any) bool {
	key, ok := validWidgetSnapshot(event, raw)
	if !ok {
		return false
	}
	q.pending[key] = append(json.RawMessage(nil), raw...)
	return true
}

func (q *widgetNotificationQueue) flush(send func(json.RawMessage)) {
	if len(q.pending) == 0 {
		return
	}
	keys := make([]string, 0, len(q.pending))
	for key := range q.pending {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		send(q.pending[key])
	}
	q.pending = make(map[string]json.RawMessage)
}

func (q *widgetNotificationQueue) clear() {
	q.pending = make(map[string]json.RawMessage)
}

func validWidgetSnapshot(event map[string]any, raw json.RawMessage) (string, bool) {
	if stringValue(event["type"]) != "extension_ui_request" || stringValue(event["method"]) != "setWidget" {
		return "", false
	}
	key := stringValue(event["widgetKey"])
	if key == "" || len([]byte(key)) > maxWidgetKeyBytes || len(raw) > maxWidgetNotificationBytes || !json.Valid(raw) {
		return "", false
	}
	lines, present := event["widgetLines"]
	if !present || lines == nil {
		// A nil/missing widgetLines is the deletion snapshot and must be
		// forwarded rather than filtered as an empty update.
		return key, true
	}
	var values []any
	switch typed := lines.(type) {
	case []any:
		values = typed
	case []string:
		values = make([]any, len(typed))
		for index, line := range typed {
			values[index] = line
		}
	default:
		return "", false
	}
	if len(values) > maxWidgetLines {
		return "", false
	}
	for _, value := range values {
		line, ok := value.(string)
		if !ok || len([]byte(line)) > maxWidgetLineBytes {
			return "", false
		}
	}
	return key, true
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

func (s *Server) tryAcquireAdmission() bool {
	if s.admission == nil {
		return true
	}
	select {
	case s.admission <- struct{}{}:
		return true
	default:
		log.Printf("[subagent] detached run queue full: capacity=%d", cap(s.admission))
		return false
	}
}

func (s *Server) releaseAdmission() {
	if s.admission != nil {
		<-s.admission
	}
}

// finishIfAborted closes the same durable lifecycle as every other early
// failure. It is intentionally called at the boundaries between queueing,
// preparation, process start, and command delivery to close cancellation
// windows without adding another polling goroutine.
func (s *Server) finishIfAborted(runCtx context.Context, runCancel context.CancelFunc, runDir string, record RunRecord) (RunResult, error, bool) {
	if runCtx.Err() == nil && !abortRequested(runDir) {
		return RunResult{}, nil, false
	}
	runCancel()
	result := s.finishFailed(runDir, record, "aborted", errors.New("subagent run aborted"))
	stopErr := runCtx.Err()
	if stopErr == nil {
		stopErr = context.Canceled
	}
	return result, stopErr, true
}

func (s *Server) requestAbort(runID string) bool {
	s.activeMu.Lock()
	runCancel := s.active[runID]
	s.activeMu.Unlock()
	if runCancel != nil {
		runCancel()
		return true
	}
	runDir, err := containedRunDir(s.snapshot.SessionDir, runID)
	if err != nil {
		return false
	}
	if abortRequested(runDir) {
		return true
	}
	record, err := ReadRunRecord(filepath.Join(runDir, "run.json"))
	if err != nil || record.Status != "running" {
		return false
	}
	// The marker is a presence-only mailbox. Store it through the existing
	// atomic JSON writer so the fallback path is safe on Windows as well.
	if err := writeJSONAtomic(filepath.Join(runDir, ".abort"), map[string]any{
		"requestedAt": time.Now().UnixMilli(),
	}); err != nil {
		log.Printf("[subagent %s] write abort marker: %v", runID, err)
		return false
	}
	return true
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
	case "extension_ui_request":
		// setWidget snapshots are validated, coalesced, and persisted by the
		// widget queue. Persisting them here would restore the original
		// high-frequency, unbounded event log growth.
		return stringValue(event["method"]) != "setWidget"
	case "subagent_ui_response":
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
	invalidResponseDeadline := time.Time{}
	readRetryDeadline := time.Time{}
	var readRetryErr error
	for {
		if !readRetryDeadline.IsZero() && !time.Now().Before(readRetryDeadline) {
			return nil, readRetryErr
		}
		raw, err := os.ReadFile(responsePath)
		if err == nil {
			readRetryDeadline = time.Time{}
			readRetryErr = nil
			var response map[string]any
			if unmarshalErr := json.Unmarshal(raw, &response); unmarshalErr != nil {
				// The mailbox writer may have created/truncated the file and be
				// writing its JSON body concurrently. Do not turn that transient
				// half-document into a protocol failure; retry briefly. This is
				// bounded so a genuinely malformed response still fails closed.
				if invalidResponseDeadline.IsZero() {
					invalidResponseDeadline = time.Now().Add(uiResponseRetryWindow)
				}
				if !time.Now().Before(invalidResponseDeadline) {
					return nil, errors.New("invalid subagent UI response")
				}
			} else {
				invalidResponseDeadline = time.Time{}
				if stringValue(response["id"]) != requestID {
					return nil, errors.New("invalid subagent UI response")
				}
				_ = os.Remove(responsePath)
				return response, nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			// A writer can briefly hold or replace the mailbox while the file
			// is being materialized. Retry that read failure for the same bounded
			// window used for incomplete JSON, then return the original error.
			// Do not continue here: fall through so the retry is throttled by the
			// ticker below instead of busy-spinning for the whole retry window.
			if readRetryDeadline.IsZero() {
				readRetryDeadline = time.Now().Add(uiResponseRetryWindow)
				readRetryErr = err
			}
		}
		if !acknowledged {
			// Phase 1: wait for the frontend to acknowledge rendering (or answer
			// directly). Once acknowledged, uiResponseTimeout no longer applies.
			if _, statErr := os.Stat(ackPath); statErr == nil {
				acknowledged = true
				// Re-check the response immediately only when it simply does not
				// exist yet (no I/O cost); when the last read failed for another
				// reason the writer is still materializing it, so fall through to
				// the throttled select instead of spinning on a held file.
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return nil, statErr
			}
		}
		// The run loop is blocked in this function while the dialog is open, so
		// its own abort ticker cannot run. Poll the abort marker here instead:
		// the user clicking stop must stop the subagent immediately. The run loop
		// records context.Canceled as "aborted".
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
		if err := finishChildNode(runDir, record.ChildNodeIDs[0], status, endedAt); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("[subagent %s] finish failed child change node: %v", record.RunID, err)
		}
	}
	if err := cleanupRunMarkers(runDir); err != nil {
		log.Printf("[subagent %s] clean failed run markers: %v", record.RunID, err)
	}
	causeText := "subagent run failed"
	if cause != nil {
		causeText = cause.Error()
	}
	record.Status, record.Error, record.EndedAt = status, causeText, endedAt
	record.Files = collectRunFiles(runDir, s.snapshot.WorkDir)
	if err := writeRunRecord(runDir, record); err != nil {
		log.Printf("[subagent %s] persist failed run record: %v", record.RunID, err)
	}
	return RunResult{
		RunID: record.RunID, AgentKey: record.AgentKey, ParentNodeID: record.ParentNodeID,
		Status: status, Files: record.Files, Error: causeText, Transcript: runDir,
	}
}

func cleanupRunMarkers(runDir string) error {
	var result error
	for _, name := range []string{".abort", ".active-change-node"} {
		if err := os.Remove(filepath.Join(runDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (s *Server) writeError(id, code, message string) {
	s.writeErrorRetryable(id, code, message, false)
}

func (s *Server) writeErrorRetryable(id, code, message string, retryable ...bool) {
	canRetry := len(retryable) > 0 && retryable[0]
	s.writeResponse(Response{
		Version: ProtocolVersion, ID: id, OK: false,
		Error: &ResponseError{Code: code, Message: message, Retryable: canRetry},
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
