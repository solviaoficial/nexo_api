package wa

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types/events"
	"nexo.local/whatsapp/internal/core"
)

func TestSuccessfulDialKeepsSocketContextAlive(t *testing.T) {
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	var socketContext context.Context
	cancel, e := dialLifetime(parent, 10*time.Millisecond, func(ctx context.Context) error { socketContext = ctx; return nil })
	if e != nil {
		t.Fatal(e)
	}
	defer cancel()
	time.Sleep(30 * time.Millisecond)
	if socketContext.Err() != nil {
		t.Fatal("socket expired with handshake timeout")
	}
	stop()
	if socketContext.Err() == nil {
		t.Fatal("socket outlived service")
	}
}
func TestDialTimeoutCancelsHandshake(t *testing.T) {
	_, e := dialLifetime(context.Background(), time.Millisecond, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	if e == nil {
		t.Fatal("hung handshake accepted")
	}
}
func TestFailedDialCancelsContext(t *testing.T) {
	var socketContext context.Context
	_, e := dialLifetime(context.Background(), time.Second, func(ctx context.Context) error { socketContext = ctx; return errors.New("network") })
	if e == nil || socketContext.Err() == nil {
		t.Fatal("failed handshake leaked context")
	}
}
func TestPersistedSessionOptionsAndConflictPause(t *testing.T) {
	dir := t.TempDir()
	s, e := core.Open(filepath.Join(dir, "app.db"), bytes.Repeat([]byte{4}, 32), false)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, e := New(ctx, dir, s)
	if e != nil {
		t.Fatal(e)
	}
	if !m.client.UseRetryMessageStore || !m.client.EnableDecryptedEventBuffer || m.client.EnableAutoReconnect {
		t.Fatal("incorrect session persistence options")
	}
	if !m.event(&events.StreamReplaced{}) {
		t.Fatal("failed to persist event")
	}
	if m.Ready() || m.Snapshot().State != "paused" {
		t.Fatal("conflict did not pause")
	}
	m.Close()
	m, e = New(ctx, dir, s)
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	if !m.blocked || m.Snapshot().State != "paused" {
		t.Fatal("pause lost after restart")
	}
}
