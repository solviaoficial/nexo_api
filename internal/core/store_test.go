package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T, hook bool) *Store {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "app.db"), bytes.Repeat([]byte{7}, 32), hook)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.DB.Close() })
	return s
}
func enqueue(t *testing.T, s *Store) Message {
	t.Helper()
	m, _, e := s.Enqueue(ID(), Payload{To: "5511999999999", Kind: "text", Text: "conteúdo privado"}, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func TestIdempotencyConflictAndConcurrency(t *testing.T) {
	s := testStore(t, false)
	p := Payload{To: "5511999999999", Kind: "text", Text: "oi"}
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	for range 16 {
		wg.Go(func() {
			m, _, e := s.Enqueue("request-123", p, time.Hour)
			if e != nil {
				t.Error(e)
				return
			}
			ids <- m.ID
		})
	}
	wg.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if first != id {
			t.Fatal("duplicated message")
		}
	}
	p.Text = "outro"
	if _, _, e := s.Enqueue("request-123", p, time.Hour); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	events, e := s.Events(0)
	if e != nil || len(events) != 1 {
		t.Fatal(e, events)
	}
}
func TestPayloadEncryptedAndWrongKeyRejected(t *testing.T) {
	s := testStore(t, false)
	m := enqueue(t, s)
	var raw []byte
	if e := s.DB.QueryRow("SELECT payload FROM messages WHERE id=?", m.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(raw, []byte("conteúdo privado")) {
		t.Fatal("plaintext persisted")
	}
	other, err := s.open(append([]byte{raw[0] ^ 1}, raw[1:]...))
	if err == nil && len(other) > 0 {
		t.Fatal("tampered ciphertext accepted")
	}
}
func TestRestartRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	key := bytes.Repeat([]byte{1}, 32)
	s, e := Open(path, key, true)
	if e != nil {
		t.Fatal(e)
	}
	m := enqueue(t, s)
	if e = s.Transition(m.ID, "sending", ""); e != nil {
		t.Fatal(e)
	}
	s.DB.Close()
	s, e = Open(path, key, true)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	if e = s.Recover(); e != nil {
		t.Fatal(e)
	}
	m, e = s.Get(m.ID)
	if e != nil || m.Status != "uncertain" {
		t.Fatal(m, e)
	}
	ev, e := s.Events(0)
	if e != nil || len(ev) != 3 || ev[2].Type != "message.uncertain" {
		t.Fatal(ev, e)
	}
}
func TestWrongDatabaseKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	s, e := Open(path, bytes.Repeat([]byte{1}, 32), false)
	if e != nil {
		t.Fatal(e)
	}
	s.DB.Close()
	if s, e = Open(path, bytes.Repeat([]byte{2}, 32), false); e == nil {
		s.DB.Close()
		t.Fatal("wrong key accepted")
	}
}
func TestOutOfOrderReceipts(t *testing.T) {
	s := testStore(t, true)
	m := enqueue(t, s)
	for _, state := range []string{"sending", "read"} {
		if e := s.Transition(m.ID, state, ""); e != nil {
			t.Fatal(e)
		}
	}
	for _, state := range []string{"sent", "delivered", "uncertain", "cancelled"} {
		if e := s.Transition(m.ID, state, ""); !errors.Is(e, ErrState) {
			t.Fatal(state, e)
		}
	}
	m, _ = s.Get(m.ID)
	if m.Status != "read" {
		t.Fatal(m)
	}
}
func TestEventDeduplication(t *testing.T) {
	s := testStore(t, true)
	for range 3 {
		if e := s.Event("in:chat:id", "message.received", map[string]string{"text": "a"}); e != nil {
			t.Fatal(e)
		}
	}
	events, e := s.Events(0)
	if e != nil || len(events) != 1 {
		t.Fatal(events, e)
	}
}
func TestWebhookDeadLetter(t *testing.T) {
	s := testStore(t, true)
	enqueue(t, s)
	e, err := s.WebhookNext()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.WebhookResult(e.ID, false, 10, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = s.WebhookNext(); err == nil {
		t.Fatal("dead letter automatically retried")
	}
	if err = s.RetryWebhook(e.ID); err != nil {
		t.Fatal(err)
	}
	e, err = s.WebhookNext()
	if err != nil || e.Attempts != 0 {
		t.Fatal(e, err)
	}
}
func TestRetentionPreservesIdempotency(t *testing.T) {
	s := testStore(t, false)
	p := Payload{To: "5511999999999", Kind: "text", Text: "segredo"}
	m, _, _ := s.Enqueue("retain-123", p, time.Hour)
	s.Transition(m.ID, "sending", "")
	s.Transition(m.ID, "sent", "")
	s.DB.Exec("UPDATE messages SET updated=1")
	if e := s.Cleanup(7); e != nil {
		t.Fatal(e)
	}
	m, created, e := s.Enqueue("retain-123", p, time.Hour)
	if e != nil || created || m.Payload.Text != "" {
		t.Fatal(m, created, e)
	}
}
func TestQueueCapacity(t *testing.T) {
	s := testStore(t, false)
	for range 1000 {
		enqueue(t, s)
	}
	if _, _, e := s.Enqueue(ID(), Payload{To: "5511999999999", Kind: "text", Text: "full"}, time.Hour); !errors.Is(e, ErrFull) {
		t.Fatal(e)
	}
}

type fakeTransport struct {
	ready                 bool
	prepareErr, errorSend error
	sends                 int
	prepareCalls          int
	onSend                func()
}

func (f *fakeTransport) Ready() bool { return f.ready }
func (f *fakeTransport) Prepare(context.Context, Payload) (any, error) {
	f.prepareCalls++
	return nil, f.prepareErr
}
func (f *fakeTransport) Send(context.Context, Message, any) error {
	f.sends++
	if f.onSend != nil {
		f.onSend()
	}
	return f.errorSend
}
func TestAmbiguousSendNeverAutomaticallyRepeated(t *testing.T) {
	s := testStore(t, false)
	m := enqueue(t, s)
	f := &fakeTransport{ready: true, errorSend: context.DeadlineExceeded}
	w := &Worker{Store: s, Transport: f}
	for range 3 {
		if e := w.Step(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	m, _ = s.Get(m.ID)
	if f.sends != 1 || m.Status != "uncertain" {
		t.Fatal(f, m)
	}
}
func TestOfflineDoesNotConsumeMessage(t *testing.T) {
	s := testStore(t, false)
	m := enqueue(t, s)
	f := &fakeTransport{}
	w := &Worker{Store: s, Transport: f}
	w.Step(context.Background())
	m, _ = s.Get(m.ID)
	if m.Status != "queued" || f.sends != 0 || f.prepareCalls != 0 {
		t.Fatal(m)
	}
}
func TestPreparationFailureDoesNotSend(t *testing.T) {
	s := testStore(t, false)
	m := enqueue(t, s)
	f := &fakeTransport{ready: true, prepareErr: errors.New("upload failed")}
	w := &Worker{Store: s, Transport: f}
	if e := w.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	m, _ = s.Get(m.ID)
	if m.Status != "failed" || f.sends != 0 {
		t.Fatal(m)
	}
}
func TestExpiredMessageNotSent(t *testing.T) {
	s := testStore(t, false)
	m, _, e := s.Enqueue(ID(), Payload{To: "5511999999999", Kind: "text", Text: "oi"}, -time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	f := &fakeTransport{ready: true}
	w := &Worker{Store: s, Transport: f}
	w.Step(context.Background())
	m, _ = s.Get(m.ID)
	if m.Status != "failed" || f.sends != 0 {
		t.Fatal(m)
	}
}
func TestReceiptDuringSend(t *testing.T) {
	s := testStore(t, false)
	m := enqueue(t, s)
	f := &fakeTransport{ready: true}
	f.onSend = func() {
		if e := s.Transition(m.ID, "delivered", ""); e != nil {
			t.Fatal(e)
		}
	}
	w := &Worker{Store: s, Transport: f}
	if e := w.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	m, _ = s.Get(m.ID)
	if m.Status != "delivered" {
		t.Fatal(m)
	}
}
func TestPauseAndCancel(t *testing.T) {
	s := testStore(t, false)
	m := enqueue(t, s)
	f := &fakeTransport{ready: true}
	w := &Worker{Store: s, Transport: f}
	w.Paused.Store(true)
	w.Step(context.Background())
	if f.sends != 0 {
		t.Fatal("sent while paused")
	}
	if e := s.Transition(m.ID, "cancelled", ""); e != nil {
		t.Fatal(e)
	}
	w.Paused.Store(false)
	w.Step(context.Background())
	if f.sends != 0 {
		t.Fatal("cancelled message sent")
	}
}
func TestWebhookAuthenticatesExactBytes(t *testing.T) {
	secret := "test-secret"
	var received bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-Nexo-Signature") != Signature(secret, r.Header.Get("X-Nexo-Timestamp"), b) {
			t.Error("invalid signature")
		}
		if r.Header.Get("X-Nexo-Event-ID") != "evt-1" {
			t.Error("missing id")
		}
		received = true
		w.WriteHeader(204)
	}))
	defer srv.Close()
	if !Deliver(context.Background(), srv.Client(), srv.URL, secret, Event{ID: "evt-1", Data: json.RawMessage(`{"a":1}`)}) || !received {
		t.Fatal("not delivered")
	}
}
func TestWebhookRedirectRejected(t *testing.T) {
	var leaked bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer origin.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if Deliver(context.Background(), client, origin.URL, "secret", Event{Data: json.RawMessage(`{}`)}) || leaked {
		t.Fatal("redirect leaked payload")
	}
}
func TestSignatureIsNotPlainHash(t *testing.T) {
	b := []byte("payload")
	h := sha256.Sum256(b)
	if Signature("secret", "1", b) == "sha256="+hex.EncodeToString(h[:]) {
		t.Fatal("not keyed")
	}
	if Signature("a", "1", b) == Signature("b", "1", b) || Signature("a", "1", b) == Signature("a", "2", b) {
		t.Fatal("missing key/timestamp")
	}
}
