package steward

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

const (
	connectBackoffBase   = 2 * time.Second
	connectBackoffMax    = 60 * time.Second
	initialReadyWait     = 500 * time.Millisecond
	channelStatusRunning = "connected"
	channelStatusRetry   = "connecting"
	channelStatusStopped = "disconnected"
	channelStatusError   = "error"
	// stopTimeout bounds how long we wait for a connector's supervise goroutine
	// to exit during shutdown. A correct connector returns from Connect(ctx)
	// once ctx is cancelled, but some platform SDKs keep blocking on a long-lived
	// transport read and ignore cancellation. The shutdown path runs on the
	// application's main thread, so an unbounded wait here would leave the whole
	// CodingTo.exe process stuck in the task list after the window is closed.
	stopTimeout = 3 * time.Second
)

var (
	errUnsupportedPlatform = errors.New("steward: unsupported platform")
	errChannelNotConnected = errors.New("steward: channel is not connected")
)

// activeChannel tracks one running connector instance.
type activeChannel struct {
	channelID int64
	platform  Platform
	connector Connector
	cancel    context.CancelFunc
	done      chan struct{}
	status    string
	lastError string
	ready     chan struct{} // closed after the initial live status is published
	readyOnce sync.Once
}

// channelManager owns the lifecycle of all connector instances: start with
// reconnect backoff, stop, send, and status reporting.
type channelManager struct {
	mu        sync.Mutex
	factories map[Platform]ConnectorFactory
	channels  map[int64]*activeChannel
	onMessage func(InboundMessage)
	onStatus  func(channelID int64, status, lastError string)
	logger    *log.Logger
}

func newChannelManager(factories map[Platform]ConnectorFactory, onMessage func(InboundMessage), logger *log.Logger) *channelManager {
	return &channelManager{
		factories: factories,
		channels:  make(map[int64]*activeChannel),
		onMessage: onMessage,
		logger:    logger,
	}
}

// start launches a connector for the channel; it replaces any existing one.
func (m *channelManager) start(channelID int64, platform Platform, config, secrets map[string]string) error {
	// Replacing a live connector must use the same bounded shutdown path as an
	// explicit stop. Some SDKs ignore context cancellation until Close is called;
	// waiting on done directly here can otherwise wedge channel edits forever.
	m.stop(channelID)

	m.mu.Lock()
	factory := m.factories[platform]
	if factory == nil {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", errUnsupportedPlatform, platform)
	}
	connector, err := factory(config, secrets, m.onMessage)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := &activeChannel{
		channelID: channelID, platform: platform, connector: connector,
		cancel: cancel, done: make(chan struct{}), ready: make(chan struct{}), status: channelStatusRetry,
	}
	m.channels[channelID] = ch
	m.mu.Unlock()

	go m.supervise(ctx, ch)
	// Give fast connectors a chance to publish their initial state, but never
	// let an unreachable provider stall application startup indefinitely. A
	// slow connector remains "connecting" and publishes ready asynchronously.
	select {
	case <-ch.ready:
	case <-time.After(initialReadyWait):
	}
	return nil
}

// supervise keeps the connector alive with reconnect backoff until stop.
func (m *channelManager) supervise(ctx context.Context, ch *activeChannel) {
	defer close(ch.done)
	backoff := connectBackoffBase
	for {
		// The connector announces when outbound sends are safe. In particular,
		// Feishu creates its REST client inside Connect; publishing "connected"
		// before entering Connect made startup notices race that initialization.
		var announceReady sync.Once
		err := ch.connector.Connect(ctx, func() {
			announceReady.Do(func() {
				m.setStatus(ch.channelID, channelStatusRunning, "")
				ch.readyOnce.Do(func() { close(ch.ready) })
			})
		})
		if ctx.Err() != nil {
			m.setStatus(ch.channelID, channelStatusStopped, "")
			ch.readyOnce.Do(func() { close(ch.ready) })
			return
		}
		if err != nil {
			m.setStatus(ch.channelID, channelStatusRetry, err.Error())
			m.logger.Printf("channel connection error: channel=%d platform=%s error=%v retryIn=%s", ch.channelID, ch.platform, err, backoff)
		} else {
			backoff = connectBackoffBase
			// Connect returning means the transport ended. It is no longer
			// connected while the supervisor waits before retrying.
			m.setStatus(ch.channelID, channelStatusRetry, "")
			m.logger.Printf("channel connection closed: channel=%d platform=%s retryIn=%s", ch.channelID, ch.platform, backoff)
		}
		// Unblock the initial start call even when the first connection attempt
		// fails before it can become ready; the supervisor continues retrying.
		ch.readyOnce.Do(func() { close(ch.ready) })
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < connectBackoffMax {
			backoff *= 2
			if backoff > connectBackoffMax {
				backoff = connectBackoffMax
			}
		}
	}
}

// stop terminates the connector and waits for its goroutine to exit.
func (m *channelManager) stop(channelID int64) {
	m.mu.Lock()
	ch := m.channels[channelID]
	delete(m.channels, channelID)
	m.mu.Unlock()
	if ch == nil {
		return
	}
	ch.cancel()
	_ = ch.connector.Close()
	m.awaitDone(ch)
}

// awaitDone waits for the connector's supervise goroutine to exit. It is
// bounded by stopTimeout: once the connector's context is cancelled a correct
// Connect implementation returns promptly, but if a platform SDK ignores
// cancellation (e.g. a long-lived transport read) we must not block the caller
// forever — the shutdown path runs on the application's main thread, so an
// unbounded wait would leave the whole process stuck after the window closes.
func (m *channelManager) awaitDone(ch *activeChannel) {
	select {
	case <-ch.done:
	case <-time.After(stopTimeout):
		m.logger.Printf("channel stop timed out waiting for connector to exit; forcing shutdown: channel=%d platform=%s", ch.channelID, ch.platform)
	}
}

// stopAll terminates every running connector.
func (m *channelManager) stopAll() {
	m.mu.Lock()
	channels := make([]*activeChannel, 0, len(m.channels))
	for _, ch := range m.channels {
		channels = append(channels, ch)
	}
	m.channels = make(map[int64]*activeChannel)
	m.mu.Unlock()
	for _, ch := range channels {
		ch.cancel()
		_ = ch.connector.Close()
	}
	for _, ch := range channels {
		m.awaitDone(ch)
	}
}

// send delivers an outbound message through the channel connector.
func (m *channelManager) send(channelID int64, msg OutboundMessage) error {
	return m.sendWithTimeout(channelID, msg, 15*time.Second)
}

func (m *channelManager) sendWithTimeout(channelID int64, msg OutboundMessage, timeout time.Duration) error {
	m.mu.Lock()
	ch := m.channels[channelID]
	m.mu.Unlock()
	if ch == nil {
		return fmt.Errorf("%w: %d", errChannelNotConnected, channelID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return ch.connector.Send(ctx, msg)
}

// connector returns the active connector for a channel, if any. Callers must
// not retain it: it may be replaced by a reconnect at any time.
func (m *channelManager) connector(channelID int64) (Connector, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch := m.channels[channelID]; ch != nil {
		return ch.connector, true
	}
	return nil, false
}

// status returns the live status and whether the channel has an active
// connector. A channel that is not present in the manager is disconnected;
// callers can use active to distinguish that from a persisted startup error.
func (m *channelManager) status(channelID int64) (string, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch := m.channels[channelID]; ch != nil {
		return ch.status, ch.lastError, true
	}
	return channelStatusStopped, "", false
}

func (m *channelManager) setStatus(channelID int64, status, lastError string) {
	m.mu.Lock()
	var notify func(int64, string, string)
	if ch := m.channels[channelID]; ch != nil {
		ch.status = status
		ch.lastError = lastError
		notify = m.onStatus
	}
	m.mu.Unlock()
	if notify != nil {
		notify(channelID, status, lastError)
	}
}
