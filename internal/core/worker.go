package core

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// Prepare may upload a file, but never sends a chat message. Send has an ambiguous
// outcome on any error and MUST NOT be automatically retried by the application.
type Transport interface {
	Ready() bool
	Prepare(context.Context, Payload) (any, error)
	Send(context.Context, Message, any) error
}
type Worker struct {
	Store     *Store
	Transport Transport
	Interval  time.Duration
	Beat      atomic.Int64
	Paused    atomic.Bool
}

func (w *Worker) Step(ctx context.Context) error {
	w.Beat.Store(time.Now().Unix())
	if w.Paused.Load() || !w.Transport.Ready() {
		return nil
	}
	m, err := w.Store.Next()
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if time.Now().Unix() >= m.Expires {
		return w.Store.Transition(m.ID, "failed", "prazo de envio expirado")
	}
	// Claim before uploads so cancelling cannot race with a send.
	if err = w.Store.Transition(m.ID, "sending", ""); err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	prepared, err := w.Transport.Prepare(sendCtx, m.Payload)
	if err != nil {
		return w.Store.Transition(m.ID, "failed", "falha ao preparar arquivo; mensagem não enviada")
	}
	err = w.Transport.Send(sendCtx, m, prepared)
	if err != nil {
		err = w.Store.Transition(m.ID, "uncertain", "sem confirmação do servidor; confira no telefone antes de repetir")
	} else {
		err = w.Store.Transition(m.ID, "sent", "")
	}
	// A receipt may already have advanced the state while Send was in flight.
	if errors.Is(err, ErrState) {
		return nil
	}
	return err
}
func (w *Worker) Run(ctx context.Context) {
	w.Beat.Store(time.Now().Unix())
	t := time.NewTicker(time.Second)
	defer t.Stop()
	next := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Beat.Store(time.Now().Unix())
			if time.Now().Before(next) {
				continue
			}
			if err := w.Step(ctx); err != nil {
				slog.Error("queue_step_failed", "error", err)
				w.Paused.Store(true)
				if persistErr := w.Store.SetSetting("queue_paused", "true"); persistErr != nil {
					slog.Error("persist_queue_pause_failed")
				}
			}
			next = time.Now().Add(w.Interval)
		}
	}
}
func Signature(secret, timestamp string, b []byte) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(timestamp + "."))
	h.Write(b)
	return "sha256=" + hex.EncodeToString(h.Sum(nil))
}
func Backoff(attempt int) time.Duration {
	if attempt > 10 {
		attempt = 10
	}
	return time.Duration(1<<attempt)*time.Second + time.Duration(rand.IntN(1000))*time.Millisecond
}
func Deliver(ctx context.Context, client *http.Client, url, secret string, e Event) bool {
	b, err := json.Marshal(e)
	if err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return false
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Nexo-Event-ID", e.ID)
	req.Header.Set("X-Nexo-Timestamp", ts)
	req.Header.Set("X-Nexo-Signature", Signature(secret, ts, b))
	res, err := client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	return res.StatusCode >= 200 && res.StatusCode < 300
}
func Webhooks(ctx context.Context, s *Store, url, secret string) {
	if url == "" {
		return
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e, err := s.WebhookNext()
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				slog.Error("webhook_read_failed")
				continue
			}
			ok := Deliver(ctx, client, url, secret, e)
			if err = s.WebhookResult(e.ID, ok, e.Attempts+1, time.Now().Add(Backoff(e.Attempts+1)).Unix()); err != nil {
				slog.Error("webhook_persist_failed")
			}
		}
	}
}
func (w *Worker) Health() error {
	if time.Now().Unix()-w.Beat.Load() > 60 {
		return fmt.Errorf("worker sem heartbeat")
	}
	return nil
}
