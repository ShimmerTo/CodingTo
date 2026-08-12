package connectors

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"codingto/internal/steward"
)

// loopback is an in-process channel used for local end-to-end testing: it
// captures outbound messages and accepts injected inbound messages.
type loopback struct {
	channelID int64
	mu        sync.Mutex
	onMessage func(steward.InboundMessage)
	outbound  []steward.OutboundMessage
}

var loopbackRegistry sync.Map // channelID(int64) -> *loopback

func loopbackFactory(config, secrets map[string]string, onMessage func(steward.InboundMessage)) (steward.Connector, error) {
	conn := &loopback{channelID: channelIDFromConfig(config), onMessage: onMessage}
	if raw := config["channelId"]; raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			loopbackRegistry.Store(id, conn)
		}
	}
	return conn, nil
}

// Connect is a no-op for loopback: it is always "connected".
func (l *loopback) Connect(ctx context.Context, ready func()) error {
	ready()
	<-ctx.Done()
	return nil
}

func (l *loopback) Send(_ context.Context, msg steward.OutboundMessage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.outbound = append(l.outbound, msg)
	return nil
}

func (l *loopback) Close() error {
	// Compare-and-delete prevents an old connector that is finishing shutdown
	// from unregistering a newer replacement for the same channel.
	loopbackRegistry.CompareAndDelete(l.channelID, l)
	return nil
}

// InjectLoopback delivers a simulated inbound message to the loopback channel.
func InjectLoopback(channelID int64, msg steward.InboundMessage) error {
	value, ok := loopbackRegistry.Load(channelID)
	if !ok {
		return fmt.Errorf("loopback channel %d is not connected", channelID)
	}
	conn := value.(*loopback)
	conn.mu.Lock()
	onMessage := conn.onMessage
	conn.mu.Unlock()
	if onMessage != nil {
		if msg.ChannelID == 0 {
			msg.ChannelID = conn.channelID
		}
		onMessage(msg)
	}
	return nil
}

// ReadLoopback returns the outbound messages captured by the loopback channel
// (for tests and the desktop console view).
func ReadLoopback(channelID int64) []steward.OutboundMessage {
	value, ok := loopbackRegistry.Load(channelID)
	if !ok {
		return nil
	}
	conn := value.(*loopback)
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return append([]steward.OutboundMessage(nil), conn.outbound...)
}
