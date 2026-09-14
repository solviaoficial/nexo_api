package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"nexo.local/whatsapp/internal/core"
	"nexo.local/whatsapp/internal/wa"
)

type connection struct{}

func (connection) Snapshot() wa.Status { return wa.Status{State: "disconnected"} }
func (connection) Connect(context.Context, string) (wa.Status, error) {
	return wa.Status{State: "pairing"}, nil
}
func (connection) Pause() {}
func setup(t *testing.T) (http.Handler, *core.Store) {
	s, e := core.Open(filepath.Join(t.TempDir(), "test.db"), bytes.Repeat([]byte{3}, 32), false)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.DB.Close() })
	w := &core.Worker{Store: s, Interval: 10 * time.Second}
	w.Beat.Store(time.Now().Unix())
	a := &API{Store: s, Worker: w, WA: connection{}, Token: strings.Repeat("t", 32), Origin: "http://localhost:8080", TTL: time.Hour}
	var static fs.FS = fstest.MapFS{"index.html": {Data: []byte("test")}}
	return a.Handler(static), s
}
func request(h http.Handler, method, path, body, token, origin, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost:8080"+path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestAuthenticationAndOrigin(t *testing.T) {
	h, _ := setup(t)
	for _, tc := range []struct {
		token, origin string
		want          int
	}{{"", "", 401}, {"wrong", "", 401}, {strings.Repeat("t", 32), "https://evil.example", 403}, {strings.Repeat("t", 32), "http://localhost:8080", 200}} {
		w := request(h, "GET", "/api/status", "", tc.token, tc.origin, "")
		if w.Code != tc.want {
			t.Fatal(w.Code, tc.want)
		}
	}
}
func TestHTTPIdempotencyAndValidation(t *testing.T) {
	h, _ := setup(t)
	token := strings.Repeat("t", 32)
	body := `{"to":"5511999999999","text":"oi"}`
	w := request(h, "POST", "/api/messages", body, token, "", "request-123")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var a core.Message
	json.Unmarshal(w.Body.Bytes(), &a)
	w = request(h, "POST", "/api/messages", body, token, "", "request-123")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var b core.Message
	json.Unmarshal(w.Body.Bytes(), &b)
	if a.ID != b.ID {
		t.Fatal("duplicate")
	}
	w = request(h, "POST", "/api/messages", strings.Replace(body, "oi", "outro", 1), token, "", "request-123")
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
}
func TestRejectMalformedInputs(t *testing.T) {
	h, _ := setup(t)
	token := strings.Repeat("t", 32)
	for _, body := range []string{`{"to":"+5511","text":"oi"}`, `{"to":"5511999999999","text":""}`, `{"to":"5511999999999","text":"oi","unknown":true}`, `{"to":"5511999999999","text":"oi"}{}`, `{"to":"5511999999999","kind":"document","mime":"application/pdf","data":"YWJj","filename":"../a.pdf"}`} {
		w := request(h, "POST", "/api/messages", body, token, "", core.ID())
		if w.Code != 400 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
}
func TestNoSecretsInStatusAndSecurityHeaders(t *testing.T) {
	h, _ := setup(t)
	w := request(h, "GET", "/api/status", "", strings.Repeat("t", 32), "", "")
	if strings.Contains(w.Body.String(), strings.Repeat("t", 32)) {
		t.Fatal("token leak")
	}
	if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("security headers missing")
	}
}
func TestPausePersists(t *testing.T) {
	h, s := setup(t)
	w := request(h, "POST", "/api/queue", `{"paused":true}`, strings.Repeat("t", 32), "", "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	value, e := s.Setting("queue_paused")
	if e != nil || value != "true" {
		t.Fatal(value, e)
	}
}
func TestPhoneValidation(t *testing.T) {
	for _, n := range []string{"+5511999999999", "0012345678", "5511 1234", "123@s.whatsapp.net", "1234567"} {
		if ValidPhone(n) {
			t.Fatal(n)
		}
	}
	if !ValidPhone("5511999999999") {
		t.Fatal("valid phone rejected")
	}
}
func TestRebindingRejected(t *testing.T) {
	h, _ := setup(t)
	r := httptest.NewRequest("GET", "http://evil.example/api/status", nil)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("t", 32))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 421 {
		t.Fatal(w.Code)
	}
}
