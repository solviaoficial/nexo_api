package core

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("chave de idempotência já usada com outro conteúdo")
var ErrState = errors.New("operação não permitida neste estado")
var ErrFull = errors.New("fila cheia: aguarde ou cancele mensagens pendentes")

type Payload struct {
	To       string `json:"to"`
	Text     string `json:"text,omitempty"`
	Kind     string `json:"kind"`
	Data     []byte `json:"data,omitempty"`
	MIME     string `json:"mime,omitempty"`
	Filename string `json:"filename,omitempty"`
}
type Message struct {
	ID      string  `json:"id"`
	To      string  `json:"to"`
	Kind    string  `json:"kind"`
	Status  string  `json:"status"`
	Error   string  `json:"error,omitempty"`
	Created int64   `json:"created"`
	Updated int64   `json:"updated"`
	Expires int64   `json:"expires"`
	Payload Payload `json:"-"`
}
type Event struct {
	Seq      int64           `json:"seq"`
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Created  int64           `json:"created"`
	Data     json.RawMessage `json:"data"`
	Delivery string          `json:"delivery"`
	Attempts int             `json:"attempts"`
}
type Store struct {
	DB      *sql.DB
	aead    cipher.AEAD
	Webhook bool
}

func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func Open(path string, key []byte, webhook bool) (*Store, error) {
	if len(key) != 32 {
		return nil, errors.New("DATA_KEY deve conter 32 bytes em base64")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.ToSlash(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	s := &Store{db, aead, webhook}
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;
 CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS messages (
 id TEXT PRIMARY KEY, idem TEXT UNIQUE NOT NULL, hash TEXT NOT NULL, recipient TEXT NOT NULL,
 kind TEXT NOT NULL, payload BLOB NOT NULL, status TEXT NOT NULL, error TEXT NOT NULL DEFAULT '',
 created INTEGER NOT NULL, updated INTEGER NOT NULL, expires INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS queue ON messages(status,created);
 CREATE TABLE IF NOT EXISTS events (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT UNIQUE NOT NULL,event_key TEXT UNIQUE NOT NULL,
 type TEXT NOT NULL,payload BLOB NOT NULL,created INTEGER NOT NULL,delivery TEXT NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0);
 CREATE INDEX IF NOT EXISTS outbox ON events(delivery,next_at);
 PRAGMA user_version=1;`)
	if err != nil {
		db.Close()
		return nil, err
	}
	var check []byte
	err = db.QueryRow("SELECT value FROM metadata WHERE key='key_check'").Scan(&check)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = db.Exec("INSERT INTO metadata(key,value) VALUES('key_check',?)", s.seal([]byte("nexo-key-v1")))
	} else if err == nil {
		var v []byte
		v, err = s.open(check)
		if err == nil && string(v) != "nexo-key-v1" {
			err = errors.New("DATA_KEY incorreta")
		}
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("verificar chave do banco: %w", err)
	}
	return s, nil
}
func (s *Store) seal(b []byte) []byte {
	n := make([]byte, s.aead.NonceSize())
	if _, e := rand.Read(n); e != nil {
		panic(e)
	}
	return s.aead.Seal(n, n, b, nil)
}
func (s *Store) open(b []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(b) < n {
		return nil, errors.New("payload corrompido")
	}
	return s.aead.Open(nil, b[:n], b[n:], nil)
}

type execer interface {
	Exec(string, ...any) (sql.Result, error)
}

func (s *Store) event(tx execer, key, kind string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	delivery := "disabled"
	if s.Webhook {
		delivery = "pending"
	}
	_, err = tx.Exec("INSERT OR IGNORE INTO events(id,event_key,type,payload,created,delivery) VALUES(?,?,?,?,?,?)", ID(), key, kind, s.seal(b), time.Now().Unix(), delivery)
	return err
}
func (s *Store) Event(key, kind string, data any) error { return s.event(s.DB, key, kind, data) }
func (s *Store) Enqueue(idem string, p Payload, ttl time.Duration) (Message, bool, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return Message{}, false, err
	}
	h := sha256.Sum256(b)
	hash := hex.EncodeToString(h[:])
	tx, err := s.DB.Begin()
	if err != nil {
		return Message{}, false, err
	}
	defer tx.Rollback()
	var oldID, oldHash string
	err = tx.QueryRow("SELECT id,hash FROM messages WHERE idem=?", idem).Scan(&oldID, &oldHash)
	if err == nil {
		tx.Rollback()
		if hash != oldHash {
			return Message{}, false, ErrConflict
		}
		m, e := s.Get(oldID)
		return m, false, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, err
	}
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM messages WHERE status IN ('queued','sending')").Scan(&count); err != nil {
		return Message{}, false, err
	}
	if count >= 1000 {
		return Message{}, false, ErrFull
	}
	now := time.Now().Unix()
	m := Message{ID: ID(), To: p.To, Kind: p.Kind, Status: "queued", Created: now, Updated: now, Expires: time.Now().Add(ttl).Unix(), Payload: p}
	_, err = tx.Exec("INSERT INTO messages(id,idem,hash,recipient,kind,payload,status,created,updated,expires) VALUES(?,?,?,?,?,?,'queued',?,?,?)", m.ID, idem, hash, p.To, p.Kind, s.seal(b), now, now, m.Expires)
	if err == nil {
		err = s.event(tx, "queued:"+m.ID, "message.queued", m)
	}
	if err == nil {
		err = tx.Commit()
	}
	return m, true, err
}

const cols = "id,recipient,kind,status,error,created,updated,expires,payload"

type scanner interface{ Scan(...any) error }

func (s *Store) scan(row scanner) (Message, error) {
	var m Message
	var b []byte
	err := row.Scan(&m.ID, &m.To, &m.Kind, &m.Status, &m.Error, &m.Created, &m.Updated, &m.Expires, &b)
	if err != nil {
		return m, err
	}
	b, err = s.open(b)
	if err == nil {
		err = json.Unmarshal(b, &m.Payload)
	}
	return m, err
}
func (s *Store) Get(id string) (Message, error) {
	return s.scan(s.DB.QueryRow("SELECT "+cols+" FROM messages WHERE id=?", id))
}
func (s *Store) List() (out []Message, err error) {
	out = []Message{}
	rows, err := s.DB.Query("SELECT id,recipient,kind,status,error,created,updated,expires FROM messages ORDER BY created DESC,rowid DESC LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Message
		e := rows.Scan(&m.ID, &m.To, &m.Kind, &m.Status, &m.Error, &m.Created, &m.Updated, &m.Expires)
		if e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) Next() (Message, error) {
	return s.scan(s.DB.QueryRow("SELECT " + cols + " FROM messages WHERE status='queued' ORDER BY created,rowid LIMIT 1"))
}

// Atomic state change + transactional outbox. Receipt order can never regress a message.
func (s *Store) Transition(id, state, detail string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old string
	if err = tx.QueryRow("SELECT status FROM messages WHERE id=?", id).Scan(&old); err != nil {
		return err
	}
	allowed := false
	switch state {
	case "sending":
		allowed = old == "queued"
	case "sent":
		allowed = old == "sending" || old == "uncertain"
	case "uncertain":
		allowed = old == "sending"
	case "failed":
		allowed = old == "queued" || old == "sending"
	case "cancelled":
		allowed = old == "queued"
	case "delivered":
		allowed = old == "sending" || old == "sent" || old == "uncertain"
	case "read":
		allowed = old == "sending" || old == "sent" || old == "delivered" || old == "uncertain"
	}
	if !allowed {
		return ErrState
	}
	_, err = tx.Exec("UPDATE messages SET status=?,error=?,updated=? WHERE id=?", state, detail, time.Now().Unix(), id)
	if err == nil {
		err = s.event(tx, "state:"+id+":"+state, "message."+state, map[string]string{"id": id, "status": state, "error": detail})
	}
	if err == nil {
		err = tx.Commit()
	}
	return err
}
func (s *Store) Recover() error {
	rows, err := s.DB.Query("SELECT id FROM messages WHERE status='sending'")
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = s.Transition(id, "uncertain", "processo interrompido durante envio; confira no telefone antes de repetir"); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) Events(after int64) ([]Event, error) {
	out := []Event{}
	rows, err := s.DB.Query("SELECT seq,id,type,payload,created,delivery,attempts FROM events WHERE seq>? ORDER BY seq LIMIT 100", after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e Event
		var b []byte
		if err = rows.Scan(&e.Seq, &e.ID, &e.Type, &b, &e.Created, &e.Delivery, &e.Attempts); err != nil {
			return nil, err
		}
		e.Data, err = s.open(b)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) WebhookNext() (Event, error) {
	var e Event
	var b []byte
	err := s.DB.QueryRow("SELECT seq,id,type,payload,created,delivery,attempts FROM events WHERE delivery='pending' AND next_at<=? ORDER BY seq LIMIT 1", time.Now().Unix()).Scan(&e.Seq, &e.ID, &e.Type, &b, &e.Created, &e.Delivery, &e.Attempts)
	if err == nil {
		e.Data, err = s.open(b)
	}
	return e, err
}
func (s *Store) WebhookResult(id string, ok bool, attempt int, next int64) error {
	status := "pending"
	if ok {
		status = "delivered"
	} else if attempt >= 10 {
		status = "dead"
	}
	_, err := s.DB.Exec("UPDATE events SET delivery=?,attempts=?,next_at=? WHERE id=?", status, attempt, next, id)
	return err
}
func (s *Store) RetryWebhook(id string) error {
	r, e := s.DB.Exec("UPDATE events SET delivery='pending',attempts=0,next_at=0 WHERE id=? AND delivery='dead'", id)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e == nil && n != 1 {
		return ErrState
	}
	return e
}
func (s *Store) Setting(key string) (string, error) {
	var b []byte
	err := s.DB.QueryRow("SELECT value FROM metadata WHERE key=?", key).Scan(&b)
	return string(b), err
}
func (s *Store) SetSetting(key, value string) error {
	_, err := s.DB.Exec("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, []byte(value))
	return err
}
func (s *Store) Cleanup(days int) error {
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
	// Idempotency records are retained even after payload erasure, so old retries cannot send again.
	_, err := s.DB.Exec("UPDATE messages SET payload=? WHERE updated<? AND status IN ('read','delivered','sent','failed','cancelled')", s.seal([]byte(`{}`)), cutoff)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("DELETE FROM events WHERE created<? AND delivery IN ('delivered','disabled')", cutoff)
	return err
}
