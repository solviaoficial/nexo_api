package wa

import (
	"context"
	"time"
)

// The handshake is bounded, but the successful socket belongs to the service,
// never the HTTP request. Cancelling a request must not kill the WhatsApp socket.
func dialLifetime(parent context.Context, timeout time.Duration, connect func(context.Context) error) (context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(parent)
	timer := time.AfterFunc(timeout, cancel)
	err := connect(ctx)
	stopped := timer.Stop()
	if err == nil && !stopped {
		err = context.DeadlineExceeded
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		cancel()
		return nil, err
	}
	return cancel, nil
}
func (m *Manager) stopConnection() {
	m.mu.Lock()
	m.generation++
	m.mu.Unlock()
	m.client.Disconnect()
	if m.connCancel != nil {
		m.connCancel()
		m.connCancel = nil
	}
}
func (m *Manager) dial() error {
	if m.connCancel != nil {
		m.connCancel()
		m.connCancel = nil
	}
	cancel, err := dialLifetime(m.ctx, 25*time.Second, m.client.ConnectContext)
	if err != nil {
		m.client.Disconnect()
		return err
	}
	m.connCancel = cancel
	return nil
}
