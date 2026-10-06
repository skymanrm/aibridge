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

// maxBodyBytes fits maxFilesBytes of attachments after base64 encoding.
const maxBodyBytes = 48 << 20

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
	// Request and Response are raw JSON for the detail view; not sent with lifecycle events.
	Request  json.RawMessage `json:"-"`
	Response json.RawMessage `json:"-"`
}

// Server exposes local AI CLIs to allowlisted web origins on the loopback interface.
type Server struct {
	registry *Registry
	studio   *Studio
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
	s := &Server{cfg: cfg, registry: registry, studio: NewStudio(cfg), slots: make(chan struct{}, cfg.MaxConcurrent), port: cfg.Port}
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
	mux.HandleFunc("POST /v1/image", s.requireToken(s.image))
	mux.HandleFunc("GET /v1/n8n", s.requireToken(s.n8nStatus))
	mux.HandleFunc("POST /v1/n8n/screenshot", s.requireToken(s.n8nScreenshot))
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

func decodeBody(w http.ResponseWriter, r *http.Request, v interface{ Validate() error }) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "Invalid JSON body: "+err.Error())
		return false
	}
	if err := v.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", err.Error())
		return false
	}
	return true
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if !decodeBody(w, r, &req) {
		return
	}
	activity := struct {
		ChatRequest
		Files []fileSummary `json:"files,omitempty"`
	}{req, summarizeFiles(req.Files)}
	s.stream(w, r, job{provider: req.Provider, model: req.Model, effort: req.Effort, req: activity,
		run: func(ctx context.Context, p Provider, model string, delta func(string)) (map[string]any, string, Usage, error) {
			req.Model = model
			result, err := p.Run(ctx, req, delta)
			return map[string]any{"text": result.Text}, result.Model, result.Usage, err
		}})
}

func (s *Server) image(w http.ResponseWriter, r *http.Request) {
	var req ImageRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if p := s.registry.Get(req.Provider); p != nil {
		if _, ok := p.(ImageGenerator); !ok {
			writeError(w, http.StatusBadRequest, "unsupported", fmt.Sprintf("Provider %q cannot generate images", req.Provider))
			return
		}
	}
	activity := struct {
		ImageRequest
		Files []fileSummary `json:"files,omitempty"`
	}{req, summarizeFiles(req.Files)}
	s.stream(w, r, job{provider: req.Provider, model: req.Model, effort: req.Effort, req: activity,
		run: func(ctx context.Context, p Provider, model string, delta func(string)) (map[string]any, string, Usage, error) {
			req.Model = model
			result, err := p.(ImageGenerator).Image(ctx, req, delta)
			return map[string]any{"text": result.Text, "images": result.Images}, result.Model, result.Usage, err
		}})
}

func (s *Server) n8nStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.studio.Status(r.Context()))
}

func (s *Server) n8nScreenshot(w http.ResponseWriter, r *http.Request) {
	var req N8nRequest
	if !decodeBody(w, r, &req) {
		return
	}
	s.stream(w, r, job{provider: req.Provider, model: req.Model, effort: req.Effort, req: req, minTimeout: studioMinTimeout,
		run: func(ctx context.Context, p Provider, model string, delta func(string)) (map[string]any, string, Usage, error) {
			req.Model = model
			result, err := s.studio.Screenshot(ctx, p, req, delta)
			return map[string]any{"text": result.Text, "images": result.Images, "workflow": result.Workflow,
				"issues": result.Issues, "notes": result.Notes, "attempts": result.Attempts,
				"n8n_version": result.N8nVersion}, result.Model, result.Usage, err
		}})
}

// job is one provider run streamed as SSE `start`, `delta`*, `done` | `error`; run returns the done payload, model and usage.
type job struct {
	provider, model, effort string
	req                     any // request as received, recorded in Activity
	minTimeout              int // seconds; raises the configured timeout for long jobs
	run                     func(ctx context.Context, p Provider, model string, delta func(string)) (map[string]any, string, Usage, error)
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request, j job) {
	// An empty provider runs the job without AI (e.g. rendering a given n8n workflow).
	var provider Provider
	if j.provider != "" {
		provider = s.registry.Get(j.provider)
		if provider == nil {
			writeError(w, http.StatusNotFound, "unknown_provider", fmt.Sprintf("Unknown provider %q", j.provider))
			return
		}
		info, _ := s.registry.Info(r.Context(), j.provider)
		if !info.Available {
			writeError(w, http.StatusServiceUnavailable, "provider_unavailable",
				fmt.Sprintf("%s is not available: %s", info.Name, info.Error))
			return
		}
		if j.model == "" {
			j.model = info.DefaultModel
		}
		if j.effort != "" && len(info.Efforts) > 0 && !slices.Contains(info.Efforts, j.effort) {
			writeError(w, http.StatusBadRequest, "validation_error", fmt.Sprintf("Unsupported effort %q", j.effort))
			return
		}
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		writeError(w, http.StatusServiceUnavailable, "busy", "All bridge slots are busy, try again shortly")
		return
	}

	timeout := max(s.config().TimeoutSec, j.minTimeout)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeout)*time.Second)
	defer cancel()
	sse, err := newSSE(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	started := time.Now()
	act := Activity{ID: s.nextID.Add(1), Origin: r.Header.Get("Origin"), Provider: j.provider, Model: j.model,
		Status: "running", StartedAt: started, Request: rawJSON(j.req)}
	s.emit(act)
	defer func() {
		act.DurationMs = time.Since(started).Milliseconds()
		s.emit(act)
	}()
	sse.send("start", map[string]string{"provider": j.provider, "model": j.model})
	done, model, usage, err := j.run(ctx, provider, j.model, func(delta string) {
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
			log.Printf("%s %s/%s cancelled by client", r.URL.Path, j.provider, j.model)
			act.Status = "cancelled"
			return
		}
		act.Status, act.Error = "error", msg
		log.Printf("%s %s/%s failed after %s: %s", r.URL.Path, j.provider, j.model, time.Since(started).Round(time.Millisecond), msg)
		errBody := map[string]string{"code": code, "message": msg}
		act.Response = rawJSON(map[string]any{"error": errBody})
		sse.send("error", errBody)
		return
	}
	if model == "" {
		model = j.model
	}
	log.Printf("%s %s/%s ok in %s (%d in / %d out tokens)", r.URL.Path, j.provider, model,
		time.Since(started).Round(time.Millisecond), usage.InputTokens, usage.OutputTokens)
	act.Status, act.Model = "ok", model
	act.InputTokens, act.OutputTokens = usage.InputTokens, usage.OutputTokens
	done["provider"], done["model"], done["usage"] = j.provider, model, usage
	act.Response = rawJSON(done)
	sse.send("done", done)
}

func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
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
