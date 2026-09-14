package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/gofrs/flock"
	"nexo.local/whatsapp/internal/core"
	"nexo.local/whatsapp/internal/httpapi"
	"nexo.local/whatsapp/internal/wa"
)

//go:embed web/*
var web embed.FS

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func secret() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.StdEncoding.EncodeToString(b)
}
func initEnv() error {
	f, e := os.OpenFile(".env", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return fmt.Errorf(".env já existe ou não pode ser criado: %w", e)
	}
	defer f.Close()
	_, e = fmt.Fprintf(f, "DOMAIN=localhost\nPUBLIC_ORIGIN=http://localhost:8080\nLISTEN_ADDR=127.0.0.1:8080\nDATA_DIR=./data\nADMIN_TOKEN=%s\nDATA_KEY=%s\nSEND_INTERVAL_SECONDS=10\nQUEUE_TTL_HOURS=24\nRETENTION_DAYS=7\nWEBHOOK_URL=\nWEBHOOK_SECRET=%s\n", secret(), secret(), secret())
	return e
}
func run() error {
	if len(os.Args) > 1 && os.Args[1] == "init-env" {
		return initEnv()
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		c := http.Client{Timeout: 3 * time.Second}
		res, e := c.Get("http://127.0.0.1:8080/readyz")
		if e != nil {
			return e
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			return errors.New("unready")
		}
		return nil
	}
	token := os.Getenv("ADMIN_TOKEN")
	if len(token) < 32 {
		return errors.New("ADMIN_TOKEN precisa de pelo menos 32 caracteres; execute init-env")
	}
	key, e := base64.StdEncoding.DecodeString(os.Getenv("DATA_KEY"))
	if e != nil || len(key) != 32 {
		return errors.New("DATA_KEY precisa de 32 bytes em base64")
	}
	origin := env("PUBLIC_ORIGIN", "http://localhost:8080")
	u, e := url.Parse(origin)
	if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("PUBLIC_ORIGIN inválida; use esquema e domínio sem barra final")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" {
		return errors.New("PUBLIC_ORIGIN deve usar HTTPS fora de localhost")
	}
	interval, e := strconv.Atoi(env("SEND_INTERVAL_SECONDS", "10"))
	if e != nil || interval < 1 || interval > 3600 {
		return errors.New("SEND_INTERVAL_SECONDS: 1..3600")
	}
	ttl, e := strconv.Atoi(env("QUEUE_TTL_HOURS", "24"))
	if e != nil || ttl < 1 || ttl > 168 {
		return errors.New("QUEUE_TTL_HOURS: 1..168")
	}
	retention, e := strconv.Atoi(env("RETENTION_DAYS", "7"))
	if e != nil || retention < 1 || retention > 365 {
		return errors.New("RETENTION_DAYS: 1..365")
	}
	hook := os.Getenv("WEBHOOK_URL")
	hookSecret := os.Getenv("WEBHOOK_SECRET")
	if hook != "" {
		v, err := url.Parse(hook)
		if err != nil || v.Scheme != "https" || v.Host == "" || v.User != nil || v.Fragment != "" || len(hookSecret) < 32 {
			return errors.New("webhook requer URL HTTPS e segredo de pelo menos 32 caracteres")
		}
	}
	dir, e := filepath.Abs(env("DATA_DIR", "./data"))
	if e != nil {
		return e
	}
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	lock := flock.New(filepath.Join(dir, "instance.lock"))
	ok, e := lock.TryLock()
	if e != nil {
		return e
	}
	if !ok {
		return errors.New("outra instância já usa este diretório; somente uma réplica é permitida")
	}
	defer lock.Unlock()
	s, e := core.Open(filepath.Join(dir, "app.db"), key, hook != "")
	if e != nil {
		return e
	}
	defer s.DB.Close()
	if e = s.Recover(); e != nil {
		return e
	}
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, e := wa.New(root, dir, s)
	if e != nil {
		return e
	}
	defer conn.Close()
	worker := &core.Worker{Store: s, Transport: conn, Interval: time.Duration(interval) * time.Second}
	worker.Beat.Store(time.Now().Unix())
	if p, err := s.Setting("queue_paused"); err == nil && p == "true" {
		worker.Paused.Store(true)
	}
	api := &httpapi.API{Store: s, Worker: worker, WA: conn, Token: token, Origin: origin, TTL: time.Duration(ttl) * time.Hour}
	static, _ := fs.Sub(web, "web")
	server := &http.Server{Addr: env("LISTEN_ADDR", "127.0.0.1:8080"), Handler: api.Handler(static), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 45 * time.Second, WriteTimeout: 50 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	var wg sync.WaitGroup
	start := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }
	start(func() { worker.Run(root) })
	start(func() { conn.Run(root) })
	start(func() { core.Webhooks(root, s, hook, hookSecret) })
	start(func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-root.Done():
				return
			case <-t.C:
				if err := s.Cleanup(retention); err != nil {
					slog.Error("retention_failed")
				}
			}
		}
	})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	failed := make(chan error, 1)
	go func() { failed <- server.ListenAndServe() }()
	slog.Info("nexo_started", "listen", server.Addr, "single_session", true)
	select {
	case <-sig:
	case err := <-failed:
		if !errors.Is(err, http.ErrServerClosed) {
			e = err
		}
	}
	shutdown, stop := context.WithTimeout(context.Background(), 50*time.Second)
	defer stop()
	server.Shutdown(shutdown)
	cancel()
	wg.Wait()
	return e
}
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if e := run(); e != nil {
		slog.Error("startup_or_shutdown_failed", "error", e)
		os.Exit(1)
	}
}
