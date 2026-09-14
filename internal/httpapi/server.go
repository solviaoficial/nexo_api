package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"context"
	"nexo.local/whatsapp/internal/core"
	"nexo.local/whatsapp/internal/wa"
)

type Connection interface {
	Snapshot() wa.Status
	Connect(context.Context, string) (wa.Status, error)
	Pause()
}
type API struct {
	Store  *core.Store
	Worker *core.Worker
	WA     Connection
	Token  string
	Origin string
	TTL    time.Duration
	mu     sync.Mutex
	budget int
	reset  time.Time
}

var phonePattern = regexp.MustCompile(`^[1-9][0-9]{7,14}$`)
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{8,128}$`)

func ValidPhone(s string) bool { return phonePattern.MatchString(s) }
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, message string) {
	write(w, status, map[string]string{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		return errors.New("use Content-Type: application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 23<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return errors.New("JSON inválido, campo desconhecido ou corpo maior que 23 MiB")
	}
	var rest any
	if d.Decode(&rest) != io.EOF {
		return errors.New("apenas um objeto JSON é permitido")
	}
	return nil
}
func (a *API) protected(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && origin != a.Origin {
			fail(w, 403, "origem não autorizada")
			return
		}
		got := sha256.Sum256([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
		want := sha256.Sum256([]byte(a.Token))
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			fail(w, 401, "token inválido")
			return
		}
		// Global per-installation API budget. No untrusted X-Forwarded-For parsing.
		a.mu.Lock()
		if time.Now().After(a.reset) {
			a.budget = 300
			a.reset = time.Now().Add(time.Minute)
		}
		a.budget--
		allowed := a.budget >= 0
		a.mu.Unlock()
		if !allowed {
			w.Header().Set("Retry-After", "60")
			fail(w, 429, "limite da API; tente novamente em 60 segundos")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (a *API) Handler(static fs.FS) http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{"connection": a.WA.Snapshot(), "queue_paused": a.Worker.Paused.Load(), "interval_seconds": a.Worker.Interval.Seconds(), "webhook_enabled": a.Store.Webhook})
	})
	api.HandleFunc("POST /api/connection/connect", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Phone string `json:"phone"`
		}
		if e := decode(w, r, &p); e != nil {
			fail(w, 400, e.Error())
			return
		}
		if p.Phone != "" && !ValidPhone(p.Phone) {
			fail(w, 400, "número internacional: somente dígitos, com DDI")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		s, e := a.WA.Connect(ctx, p.Phone)
		if e != nil {
			fail(w, 409, e.Error())
			return
		}
		write(w, 200, s)
	})
	api.HandleFunc("POST /api/connection/pause", func(w http.ResponseWriter, r *http.Request) { a.WA.Pause(); write(w, 200, a.WA.Snapshot()) })
	api.HandleFunc("POST /api/queue", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Paused *bool `json:"paused"`
		}
		if e := decode(w, r, &p); e != nil || p.Paused == nil {
			fail(w, 400, "informe paused: true ou false")
			return
		}
		if e := a.Store.SetSetting("queue_paused", strconv.FormatBool(*p.Paused)); e != nil {
			fail(w, 503, "banco indisponível")
			return
		}
		a.Worker.Paused.Store(*p.Paused)
		write(w, 200, map[string]bool{"paused": *p.Paused})
	})
	api.HandleFunc("GET /api/messages", func(w http.ResponseWriter, r *http.Request) {
		m, e := a.Store.List()
		if e != nil {
			fail(w, 503, "banco indisponível")
			return
		}
		write(w, 200, m)
	})
	api.HandleFunc("GET /api/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		m, e := a.Store.Get(r.PathValue("id"))
		if errors.Is(e, sql.ErrNoRows) {
			fail(w, 404, "mensagem não encontrada")
			return
		}
		if e != nil {
			fail(w, 503, "banco indisponível")
			return
		}
		write(w, 200, m)
	})
	api.HandleFunc("POST /api/messages", a.enqueue)
	api.HandleFunc("POST /api/messages/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		e := a.Store.Transition(r.PathValue("id"), "cancelled", "")
		if e != nil {
			fail(w, 409, "somente mensagens na fila podem ser canceladas")
			return
		}
		write(w, 200, map[string]string{"status": "cancelled"})
	})
	api.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
		after := int64(0)
		var e error
		if v := r.URL.Query().Get("after"); v != "" {
			after, e = strconv.ParseInt(v, 10, 64)
		}
		if e != nil || after < 0 {
			fail(w, 400, "cursor inválido")
			return
		}
		events, e := a.Store.Events(after)
		if e != nil {
			fail(w, 503, "banco indisponível")
			return
		}
		write(w, 200, events)
	})
	api.HandleFunc("POST /api/events/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		if !a.Store.Webhook {
			fail(w, 409, "webhook não configurado")
			return
		}
		if e := a.Store.RetryWebhook(r.PathValue("id")); e != nil {
			fail(w, 409, "somente eventos dead podem ser reprocessados")
			return
		}
		write(w, 200, map[string]string{"delivery": "pending"})
	})
	api.HandleFunc("GET /api/metrics", func(w http.ResponseWriter, r *http.Request) {
		rows, e := a.Store.DB.Query("SELECT status,count(*) FROM messages GROUP BY status")
		if e != nil {
			fail(w, 503, "banco indisponível")
			return
		}
		counts := map[string]int{}
		for rows.Next() {
			var state string
			var n int
			if e = rows.Scan(&state, &n); e != nil {
				break
			}
			counts[state] = n
		}
		rowErr := rows.Err()
		rows.Close()
		if e != nil || rowErr != nil {
			fail(w, 503, "banco indisponível")
			return
		}
		write(w, 200, counts)
	})
	mux := http.NewServeMux()
	mux.Handle("/api/", a.protected(api))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "alive"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if a.Store.DB.PingContext(r.Context()) != nil || a.Worker.Health() != nil {
			fail(w, 503, "unavailable")
			return
		}
		write(w, 200, map[string]string{"status": "ready"})
	})
	mux.Handle("/", http.FileServerFS(static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Method != "GET" && r.Method != "POST" && r.Method != "HEAD" {
			fail(w, 405, "método não permitido")
			return
		}
		// Reject unexpected Host values, including DNS rebinding against localhost.
		expected, _ := url.Parse(a.Origin)
		if expected != nil && r.Host != expected.Host && r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
			fail(w, 421, "host não autorizado")
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func (a *API) enqueue(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if !keyPattern.MatchString(key) {
		fail(w, 400, "Idempotency-Key obrigatório: 8 a 128 letras, números ou ._:-")
		return
	}
	var p core.Payload
	if err := decode(w, r, &p); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !ValidPhone(p.To) {
		fail(w, 400, "to deve conter somente dígitos, com DDI, sem +")
		return
	}
	if p.Kind == "" {
		p.Kind = "text"
	}
	if len([]rune(p.Text)) > 4096 {
		fail(w, 400, "texto maior que 4096 caracteres")
		return
	}
	if p.Kind == "text" {
		if strings.TrimSpace(p.Text) == "" || len(p.Data) > 0 {
			fail(w, 400, "texto vazio ou arquivo em mensagem de texto")
			return
		}
	} else {
		if len(p.Data) == 0 || len(p.Data) > 16<<20 {
			fail(w, 400, "arquivo deve ter entre 1 byte e 16 MiB")
			return
		}
		accepted := false
		switch p.Kind {
		case "image":
			accepted = p.MIME == "image/jpeg" || p.MIME == "image/png"
		case "video":
			accepted = p.MIME == "video/mp4"
		case "audio":
			accepted = p.MIME == "audio/mpeg" || p.MIME == "audio/ogg" || p.MIME == "audio/mp4"
		case "document":
			accepted = p.MIME == "application/pdf" || p.MIME == "text/plain"
		}
		if !accepted {
			fail(w, 400, "tipo/MIME não suportado; consulte docs/API.md")
			return
		}
		if len(p.Filename) > 200 || strings.ContainsAny(p.Filename, "/\\\r\n") {
			fail(w, 400, "nome de arquivo inválido")
			return
		}
	}
	m, created, e := a.Store.Enqueue(key, p, a.TTL)
	if errors.Is(e, core.ErrConflict) {
		fail(w, 409, e.Error())
		return
	}
	if errors.Is(e, core.ErrFull) {
		fail(w, 429, e.Error())
		return
	}
	if e != nil {
		fail(w, 503, "não foi possível persistir; repita com a mesma Idempotency-Key")
		return
	}
	status := 200
	if created {
		status = 202
	}
	write(w, status, m)
}
