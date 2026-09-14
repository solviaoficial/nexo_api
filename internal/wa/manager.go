package wa

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	"nexo.local/whatsapp/internal/core"
)

type Status struct {
	State       string `json:"state"`
	Detail      string `json:"detail"`
	QR          string `json:"qr,omitempty"`
	Code        string `json:"code,omitempty"`
	Expires     int64  `json:"expires,omitempty"`
	ConnectedAt int64  `json:"connected_at,omitempty"`
}
type Manager struct {
	op         sync.Mutex
	mu         sync.RWMutex
	client     *whatsmeow.Client
	container  *sqlstore.Container
	store      *core.Store
	ctx        context.Context
	connCancel context.CancelFunc
	status     Status
	paired     bool
	blocked    bool
	next       time.Time
	failures   int
	generation uint64
}

func New(ctx context.Context, dir string, s *core.Store) (*Manager, error) {
	db, err := sql.Open("sqlite", filepath.ToSlash(filepath.Join(dir, "session.db")))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;"); err != nil {
		db.Close()
		return nil, err
	}
	container := sqlstore.NewWithDB(db, "sqlite", waLog.Noop)
	if err = container.Upgrade(ctx); err != nil {
		db.Close()
		return nil, err
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		container.Close()
		return nil, err
	}
	m := &Manager{container: container, store: s, ctx: ctx, paired: device.ID != nil, status: Status{State: "disconnected", Detail: "Pronto para conectar"}}
	m.client = whatsmeow.NewClient(device, waLog.Noop)
	// One owner for reconnects; login's internal post-pair restart remains enabled.
	m.client.EnableAutoReconnect = false
	m.client.InitialAutoReconnect = false
	m.client.UseRetryMessageStore = true
	m.client.AutomaticMessageRerequestFromPhone = true
	m.client.EnableDecryptedEventBuffer = true
	m.client.SynchronousAck = true
	m.client.AddEventHandlerWithSuccessStatus(m.event)
	if p, e := s.Setting("connection_blocked"); e == nil && p == "true" {
		m.blocked = true
		m.status = Status{State: "paused", Detail: "Conexão pausada; retome manualmente"}
	}
	return m, nil
}
func (m *Manager) Snapshot() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.status
	if s.Expires > 0 && time.Now().Unix() >= s.Expires {
		s.QR = ""
		s.Code = ""
	}
	return s
}
func (m *Manager) Ready() bool {
	s := m.Snapshot()
	return s.State == "connected" && time.Now().Unix()-s.ConnectedAt >= 5 && m.client.IsLoggedIn() && m.client.IsConnected()
}
func (m *Manager) set(state, detail string) {
	m.mu.Lock()
	m.status.State = state
	m.status.Detail = detail
	m.mu.Unlock()
}
func (m *Manager) block(reason string) {
	m.mu.Lock()
	m.blocked = true
	m.status = Status{State: "paused", Detail: reason}
	m.mu.Unlock()
	if err := m.store.SetSetting("connection_blocked", "true"); err != nil {
		slog.Error("persist_connection_pause_failed")
	}
}
func (m *Manager) event(raw any) bool {
	var err error
	switch e := raw.(type) {
	case *events.Connected:
		m.mu.Lock()
		m.paired = true
		m.failures = 0
		if m.blocked {
			m.mu.Unlock()
			return true
		}
		m.status = Status{State: "connected", Detail: "Conectado; sincronizando sessão", ConnectedAt: time.Now().Unix()}
		m.mu.Unlock()
		err = m.store.Event(core.ID(), "connection.connected", map[string]string{"state": "connected"})
	case *events.PairSuccess:
		m.mu.Lock()
		m.paired = true
		m.status.QR = ""
		m.status.Code = ""
		m.mu.Unlock()
	case *events.Disconnected:
		m.mu.Lock()
		if !m.blocked {
			m.status = Status{State: "disconnected", Detail: "Sem conexão; retomada automática com espera progressiva"}
		}
		m.mu.Unlock()
	case *events.LoggedOut:
		m.mu.Lock()
		m.paired = false
		m.mu.Unlock()
		m.block("Sessão revogada no WhatsApp. Reinicie o serviço e faça novo pareamento.")
		err = m.store.Event(core.ID(), "connection.logged_out", map[string]string{"action": "reiniciar e parear novamente"})
	case *events.StreamReplaced:
		m.block("Outra conexão assumiu a sessão. Pare a outra instalação antes de retomar.")
		err = m.store.Event(core.ID(), "connection.conflict", map[string]string{"action": "verificar instalação duplicada"})
	case *events.ClientOutdated:
		m.block("WhatsApp recusou a versão do cliente. Atualize e valide o Whatsmeow.")
		err = m.store.Event(core.ID(), "connection.outdated", map[string]string{"action": "atualizar dependência"})
	case *events.ConnectFailure:
		m.block("Conexão recusada pelo WhatsApp. Verifique a conta e os eventos antes de retomar.")
		err = m.store.Event(core.ID(), "connection.failure", map[string]any{"reason": int(e.Reason)})
	case *events.UndecryptableMessage:
		err = m.store.Event("decrypt:"+e.Info.Chat.String()+":"+e.Info.ID, "message.undecryptable", map[string]any{"id": e.Info.ID, "chat": e.Info.Chat.String(), "unavailable": e.IsUnavailable})
	case *events.Message:
		if e.Info.IsFromMe {
			return true
		}
		text := e.Message.GetConversation()
		if text == "" {
			text = e.Message.GetExtendedTextMessage().GetText()
		}
		kind := "text"
		if e.Message.GetImageMessage() != nil {
			kind = "image"
			text = e.Message.GetImageMessage().GetCaption()
		}
		if e.Message.GetDocumentMessage() != nil {
			kind = "document"
			text = e.Message.GetDocumentMessage().GetCaption()
		}
		if e.Message.GetVideoMessage() != nil {
			kind = "video"
			text = e.Message.GetVideoMessage().GetCaption()
		}
		if e.Message.GetAudioMessage() != nil {
			kind = "audio"
		}
		if e.Message.GetProtocolMessage() != nil {
			return true
		}
		err = m.store.Event("in:"+e.Info.Chat.String()+":"+e.Info.ID, "message.received", map[string]any{"id": e.Info.ID, "chat": e.Info.Chat.String(), "sender": e.Info.Sender.String(), "timestamp": e.Info.Timestamp.Unix(), "text": text, "kind": kind})
	case *events.Receipt:
		state := ""
		if e.Type == types.ReceiptTypeDelivered {
			state = "delivered"
		}
		if e.Type == types.ReceiptTypeRead || e.Type == types.ReceiptTypePlayed {
			state = "read"
		}
		if state != "" {
			for _, id := range e.MessageIDs {
				er := m.store.Transition(id, state, "")
				if er != nil && !errors.Is(er, core.ErrState) && !errors.Is(er, sql.ErrNoRows) {
					err = er
				}
			}
		}
	}
	if err != nil {
		slog.Error("event_persist_failed", "type", fmt.Sprintf("%T", raw))
		return false
	}
	return true
}
func (m *Manager) Connect(ctx context.Context, phone string) (Status, error) {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	paired := m.paired
	s := m.status
	m.mu.Unlock()
	if s.State == "connected" {
		return s, nil
	}
	if s.State == "pairing" && s.Expires > time.Now().Unix() {
		return s, errors.New("pareamento em andamento; aguarde expirar para gerar outro")
	}
	if err := m.store.SetSetting("connection_blocked", "false"); err != nil {
		return s, err
	}
	m.mu.Lock()
	m.blocked = false
	m.mu.Unlock()
	m.stopConnection()
	if paired {
		m.set("connecting", "Retomando sessão salva")
		if err := m.dial(); err != nil {
			m.set("disconnected", "Falha ao conectar; nova tentativa automática")
			return m.Snapshot(), errors.New("não foi possível conectar")
		}
		return m.Snapshot(), nil
	}
	ch, err := m.client.GetQRChannel(m.ctx)
	if err != nil {
		return m.Snapshot(), errors.New("reinicie o serviço para iniciar um novo pareamento")
	}
	m.set("pairing", "Aguardando QR Code")
	if err = m.dial(); err != nil {
		m.stopConnection()
		m.set("disconnected", "Falha ao iniciar pareamento")
		return m.Snapshot(), errors.New("falha ao iniciar pareamento")
	}
	m.mu.RLock()
	generation := m.generation
	m.mu.RUnlock()
	first := make(chan struct{}, 1)
	go func() {
		for e := range ch {
			m.mu.Lock()
			if generation != m.generation {
				m.mu.Unlock()
				continue
			}
			if e.Event == "code" {
				png, er := qrcode.Encode(e.Code, qrcode.Medium, 320)
				if er == nil {
					m.status.QR = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
					m.status.Expires = time.Now().Add(e.Timeout).Unix()
					m.status.State = "pairing"
					m.status.Detail = "No celular: Dispositivos conectados → Conectar dispositivo"
				}
				select {
				case first <- struct{}{}:
				default:
				}
			} else if e.Event != "success" && m.status.State == "pairing" {
				m.status = Status{State: "disconnected", Detail: "Pareamento encerrado; gere um novo código"}
			}
			m.mu.Unlock()
		}
	}()
	select {
	case <-first:
	case <-ctx.Done():
		m.stopConnection()
		return m.Snapshot(), ctx.Err()
	case <-time.After(25 * time.Second):
		m.stopConnection()
		return m.Snapshot(), errors.New("WhatsApp não forneceu QR Code no prazo")
	}
	if phone != "" {
		code, e := m.client.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
		if e != nil {
			m.stopConnection()
			m.set("disconnected", "Não foi possível gerar código de pareamento")
			return m.Snapshot(), errors.New("WhatsApp recusou o pareamento por código")
		}
		m.mu.Lock()
		m.status.Code = code
		m.status.QR = ""
		m.status.Detail = "No celular: Conectar dispositivo → Conectar com número de telefone"
		m.mu.Unlock()
	}
	return m.Snapshot(), nil
}
func (m *Manager) Pause() {
	m.op.Lock()
	defer m.op.Unlock()
	m.block("Pausado por você; credenciais preservadas")
	m.stopConnection()
}
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.mu.RLock()
			attempt := m.paired && !m.blocked && m.status.State != "pairing" && time.Now().After(m.next)
			m.mu.RUnlock()
			if !attempt || m.client.IsConnected() {
				continue
			}
			if !m.op.TryLock() {
				continue
			}
			m.mu.RLock()
			blocked := m.blocked
			m.mu.RUnlock()
			if blocked {
				m.op.Unlock()
				continue
			}
			m.set("connecting", "Reconectando com sessão preservada")
			err := m.dial()
			m.mu.Lock()
			m.failures++
			m.next = time.Now().Add(min(core.Backoff(m.failures), 5*time.Minute))
			if err != nil && !m.blocked {
				m.status.State = "disconnected"
				m.status.Detail = "Tentativa falhou; aguardando próxima reconexão"
			}
			m.mu.Unlock()
			m.op.Unlock()
		}
	}
}
func (m *Manager) Close() { m.op.Lock(); defer m.op.Unlock(); m.stopConnection(); m.container.Close() }
func (m *Manager) Prepare(ctx context.Context, p core.Payload) (any, error) {
	if p.Kind == "text" {
		return &waE2E.Message{Conversation: proto.String(p.Text)}, nil
	}
	typ := whatsmeow.MediaDocument
	switch p.Kind {
	case "image":
		typ = whatsmeow.MediaImage
	case "video":
		typ = whatsmeow.MediaVideo
	case "audio":
		typ = whatsmeow.MediaAudio
	}
	upload, err := m.client.Upload(ctx, p.Data, typ)
	if err != nil {
		return nil, err
	}
	length := uint64(len(p.Data))
	msg := &waE2E.Message{}
	switch p.Kind {
	case "image":
		msg.ImageMessage = &waE2E.ImageMessage{URL: &upload.URL, DirectPath: &upload.DirectPath, MediaKey: upload.MediaKey, FileEncSHA256: upload.FileEncSHA256, FileSHA256: upload.FileSHA256, FileLength: &length, Mimetype: &p.MIME, Caption: &p.Text}
	case "video":
		msg.VideoMessage = &waE2E.VideoMessage{URL: &upload.URL, DirectPath: &upload.DirectPath, MediaKey: upload.MediaKey, FileEncSHA256: upload.FileEncSHA256, FileSHA256: upload.FileSHA256, FileLength: &length, Mimetype: &p.MIME, Caption: &p.Text}
	case "audio":
		msg.AudioMessage = &waE2E.AudioMessage{URL: &upload.URL, DirectPath: &upload.DirectPath, MediaKey: upload.MediaKey, FileEncSHA256: upload.FileEncSHA256, FileSHA256: upload.FileSHA256, FileLength: &length, Mimetype: &p.MIME, PTT: proto.Bool(false)}
	case "document":
		msg.DocumentMessage = &waE2E.DocumentMessage{URL: &upload.URL, DirectPath: &upload.DirectPath, MediaKey: upload.MediaKey, FileEncSHA256: upload.FileEncSHA256, FileSHA256: upload.FileSHA256, FileLength: &length, Mimetype: &p.MIME, Caption: &p.Text, FileName: &p.Filename}
	default:
		return nil, os.ErrInvalid
	}
	return msg, nil
}
func (m *Manager) Send(ctx context.Context, job core.Message, prepared any) error {
	msg, ok := prepared.(*waE2E.Message)
	if !ok {
		return errors.New("invalid message")
	}
	_, err := m.client.SendMessage(ctx, types.NewJID(job.To, types.DefaultUserServer), msg, whatsmeow.SendRequestExtra{ID: job.ID})
	return err
}
