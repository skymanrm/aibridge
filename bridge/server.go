package bridge

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxBodyBytes = 2 << 20

// Activity describes one chat request; emitted when it starts and again when it ends.
type Activity struct {
	ID           int64     `json:"id"`
	Origin       string    `json:"origin"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	Status       string    `json:"status"` // running | ok | error | cancelled
	Error        string    `json:"error,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	DurationMs   int64     `json:"duration_ms"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
}

// Server exposes local AI CLIs to allowlisted web origins on the loopback interface.
type Server struct {
	registry *Registry
	slots    chan struct{}
	port     int
	// OnActivity, when set, receives chat lifecycle events (called from request goroutines).
	OnActivity func(Activity)

	mu     sync.Mutex
	cfg    *Config
	cfgMod time.Time
	http   *http.Server
	nextID atomic.Int64
}

func NewServer(cfg *Config, registry *Registry) *Server {
	s := &Server{cfg: cfg, registry: registry, slots: make(chan struct{}, cfg.MaxConcurrent), port: cfg.Port}
	if st, err := os.Stat(cfg.path); err == nil {
		s.cfgMod = st.ModTime()
	}
	return s
}

// config re-reads the file when it changed, so `allow` / `token --rotate` apply without a restart.
func (s *Server) config() *Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := os.Stat(s.cfg.path)
	if err != nil || st.ModTime().Equal(s.cfgMod) {
		return s.cfg
	}
	if fresh, err := LoadConfig(); err == nil {
		s.cfg, s.cfgMod = fresh, st.ModTime()
		log.Printf("config reloaded (origins: %s)", strings.Join(fresh.Origins, ", "))
	}
	return s.cfg
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /v1/providers", s.requireToken(s.providers))
	mux.HandleFunc("POST /v1/chat", s.requireToken(s.chat))
	return s.guard(mux)
}

func (s *Server) Addr() string { return fmt.Sprintf("127.0.0.1:%d", s.port) }

// Start binds the port synchronously (so "address in use" is returned) and serves in the background.
func (s *Server) Start() (<-chan error, error) {
	ln, err := net.Listen("tcp", s.Addr())
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	s.mu.Lock()
	s.http = srv
	s.mu.Unlock()
	log.Printf("ai-bridge %s listening on http://%s (origins: %s)", Version, s.Addr(), strings.Join(s.config().Origins, ", "))
	done := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			done <- err
		}
		close(done)
	}()
	return done, nil
}

// Stop closes the listener and cancels in-flight requests (their CLI processes are killed).
func (s *Server) Stop() error {
	s.mu.Lock()
	srv := s.http
	s.http = nil
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Close()
}

func (s *Server) ListenAndServe() error {
	done, err := s.Start()
	if err != nil {
		return err
	}
	return <-done
}

func (s *Server) emit(a Activity) {
	if s.OnActivity != nil {
		s.OnActivity(a)
	}
}

// loopbackHost blocks DNS-rebinding: only literal loopback hosts are served.
func loopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.Trim(host, "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			writeError(w, http.StatusForbidden, "host_not_allowed", "Only loopback hosts are served")
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !s.config().OriginAllowed(origin) {
				writeError(w, http.StatusForbidden, "origin_not_allowed",
					fmt.Sprintf("Origin %s is not allowed; run `ai-bridge allow %s`", origin, origin))
				return
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				h.Set("Access-Control-Max-Age", "600")
				if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
					h.Set("Access-Control-Allow-Private-Network", "true")
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(s.config().Token)) == 1
}

func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Missing or invalid bridge token (see `ai-bridge token`)")
			return
		}
		next(w, r)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"name": "ai-bridge", "version": Version, "authorized": s.authorized(r)})
}

func (s *Server) providers(w http.ResponseWriter, r *http.Request) {
	infos := s.registry.Infos(r.Context(), r.URL.Query().Get("refresh") == "1")
	writeJSON(w, http.StatusOK, map[string]any{"providers": infos})
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "Invalid JSON body: "+err.Error())
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	provider := s.registry.Get(req.Provider)
	if provider == nil {
		writeError(w, http.StatusNotFound, "unknown_provider", fmt.Sprintf("Unknown provider %q", req.Provider))
		return
	}
	info, _ := s.registry.Info(r.Context(), req.Provider)
	if !info.Available {
		writeError(w, http.StatusServiceUnavailable, "provider_unavailable",
			fmt.Sprintf("%s is not available: %s", info.Name, info.Error))
		return
	}
	if req.Model == "" {
		req.Model = info.DefaultModel
	}
	if req.Effort != "" && len(info.Efforts) > 0 && !slices.Contains(info.Efforts, req.Effort) {
		writeError(w, http.StatusBadRequest, "validation_error", fmt.Sprintf("Unsupported effort %q", req.Effort))
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		writeError(w, http.StatusServiceUnavailable, "busy", "All bridge slots are busy, try again shortly")
		return
	}

	timeout := s.config().TimeoutSec
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeout)*time.Second)
	defer cancel()
	sse, err := newSSE(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	started := time.Now()
	act := Activity{ID: s.nextID.Add(1), Origin: r.Header.Get("Origin"), Provider: req.Provider, Model: req.Model,
		Status: "running", StartedAt: started}
	s.emit(act)
	defer func() {
		act.DurationMs = time.Since(started).Milliseconds()
		s.emit(act)
	}()
	sse.send("start", map[string]string{"provider": req.Provider, "model": req.Model})
	result, err := provider.Run(ctx, req, func(delta string) {
		if delta != "" {
			sse.send("delta", map[string]string{"text": delta})
		}
	})
	if err != nil {
		code, msg := "provider_failed", err.Error()
		var perr *ProviderError
		switch {
		case errors.As(err, &perr):
			code = perr.Code
		case errors.Is(err, context.DeadlineExceeded):
			code, msg = "timeout", fmt.Sprintf("No answer within %ds", timeout)
		case errors.Is(err, context.Canceled):
			log.Printf("chat %s/%s cancelled by client", req.Provider, req.Model)
			act.Status = "cancelled"
			return
		}
		act.Status, act.Error = "error", msg
		log.Printf("chat %s/%s failed after %s: %s", req.Provider, req.Model, time.Since(started).Round(time.Millisecond), msg)
		sse.send("error", map[string]string{"code": code, "message": msg})
		return
	}
	log.Printf("chat %s/%s ok in %s (%d in / %d out tokens)", req.Provider, result.Model,
		time.Since(started).Round(time.Millisecond), result.Usage.InputTokens, result.Usage.OutputTokens)
	act.Status, act.Model = "ok", result.Model
	act.InputTokens, act.OutputTokens = result.Usage.InputTokens, result.Usage.OutputTokens
	sse.send("done", map[string]any{"text": result.Text, "provider": req.Provider, "model": result.Model,
		"usage": result.Usage})
}

type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) (*sseWriter, error) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming unsupported")
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &sseWriter{w: w, f: f}, nil
}

func (s *sseWriter) send(event string, data any) {
	payload, _ := json.Marshal(data)
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, payload)
	s.f.Flush()
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
