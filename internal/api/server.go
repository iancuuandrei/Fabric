// Package api exposes Fabric's local HTTP/JSON + Server-Sent Events (SSE) API,
// embedded Mission Control dashboard, Code Knowledge Graph API, and OpenAPI 3.1.
package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iancuuandrei/fabric/internal/agentloop"
	"github.com/iancuuandrei/fabric/internal/codegraph"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/cost"
	"github.com/iancuuandrei/fabric/internal/integrations"
	"github.com/iancuuandrei/fabric/internal/mutation"
	"github.com/iancuuandrei/fabric/internal/packet"
	"github.com/iancuuandrei/fabric/internal/rewind"
	"github.com/iancuuandrei/fabric/internal/sandbox"
	"github.com/iancuuandrei/fabric/internal/webui"
	"github.com/iancuuandrei/fabric/pkg/fabric"
)

const (
	maxStoredJobs     = 256
	maxConcurrentJobs = 8
)

// ServerConfig configures the local HTTP API server.
type ServerConfig struct {
	Client    *fabric.Client
	AuthToken string
	AllowCORS bool
}

// AsyncJob tracks an asynchronous POST /v1/runs or POST /v1/episodes execution.
type AsyncJob struct {
	JobID     string          `json:"job_id"`
	RunID     string          `json:"run_id,omitempty"`
	Status    string          `json:"status"`
	StartedAt time.Time       `json:"started_at"`
	EndedAt   *time.Time      `json:"ended_at,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// episodeStream fans out real-time agentloop.Events to SSE subscribers.
type episodeStream struct {
	mu     sync.Mutex
	events []agentloop.Event
	subs   map[chan agentloop.Event]struct{}
	closed bool
}

func newEpisodeStream() *episodeStream {
	return &episodeStream{subs: make(map[chan agentloop.Event]struct{})}
}

func (es *episodeStream) publish(ev agentloop.Event) {
	es.mu.Lock()
	defer es.mu.Unlock()
	if es.closed {
		return
	}
	es.events = append(es.events, ev)
	for ch := range es.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (es *episodeStream) finish() {
	es.mu.Lock()
	defer es.mu.Unlock()
	if es.closed {
		return
	}
	es.closed = true
	for ch := range es.subs {
		close(ch)
		delete(es.subs, ch)
	}
}

func (es *episodeStream) subscribe() ([]agentloop.Event, chan agentloop.Event, bool) {
	es.mu.Lock()
	defer es.mu.Unlock()
	hist := append([]agentloop.Event(nil), es.events...)
	if es.closed {
		return hist, nil, true
	}
	ch := make(chan agentloop.Event, 64)
	es.subs[ch] = struct{}{}
	return hist, ch, false
}

func (es *episodeStream) unsubscribe(ch chan agentloop.Event) {
	es.mu.Lock()
	defer es.mu.Unlock()
	if _, ok := es.subs[ch]; ok {
		delete(es.subs, ch)
		close(ch)
	}
}

// Server serves Fabric's Mission Control UI and REST + SSE API.
type Server struct {
	client    *fabric.Client
	authToken string
	allowCORS bool
	mux       *http.ServeMux
	sem       chan struct{}

	mu        sync.RWMutex
	jobs      map[string]*AsyncJob
	jobOrder  []string
	epStreams map[string]*episodeStream
	epSteer   map[string]chan string
}

// NewServer constructs a configured Fabric HTTP API and Mission Control handler.
func NewServer(cfg ServerConfig) *Server {
	s := &Server{
		client:    cfg.Client,
		authToken: cfg.AuthToken,
		allowCORS: cfg.AllowCORS,
		mux:       http.NewServeMux(),
		sem:       make(chan struct{}, maxConcurrentJobs),
		jobs:      make(map[string]*AsyncJob),
		epStreams: make(map[string]*episodeStream),
		epSteer:   make(map[string]chan string),
	}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	ui := webui.Handler()
	s.mux.Handle("GET /{$}", ui)
	s.mux.Handle("GET /index.html", ui)
	s.mux.Handle("GET /app.js", ui)

	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /v1/openapi.json", s.handleOpenAPI)
	s.mux.HandleFunc("GET /v1/doctor", s.handleDoctor)

	// Code Knowledge Graph API (Graphify/GitNexus-grade local intelligence).
	s.mux.HandleFunc("GET /v1/graph", s.handleGraph)
	s.mux.HandleFunc("GET /v1/graph/impact", s.handleGraphImpact)

	// Classic replay-validated controller runs.
	s.mux.HandleFunc("GET /v1/runs", s.handleListRuns)
	s.mux.HandleFunc("POST /v1/runs", s.handleStartRun)
	s.mux.HandleFunc("GET /v1/jobs/{jobId}", s.handleGetJob)
	s.mux.HandleFunc("GET /v1/runs/{id}", s.handleInspectRun)
	s.mux.HandleFunc("GET /v1/runs/{id}/events", s.handleStreamEvents)
	s.mux.HandleFunc("GET /v1/runs/{id}/diff", s.handleDiff)
	s.mux.HandleFunc("GET /v1/runs/{id}/usage", s.handleUsage)
	s.mux.HandleFunc("GET /v1/runs/{id}/checkpoint", s.handleCheckpoint)
	s.mux.HandleFunc("GET /v1/runs/{id}/diagnose", s.handleDiagnose)
	s.mux.HandleFunc("POST /v1/runs/{id}/approve", s.handleApprove)
	s.mux.HandleFunc("POST /v1/runs/{id}/pause", s.handlePause)
	s.mux.HandleFunc("POST /v1/runs/{id}/resume", s.handleResume)
	s.mux.HandleFunc("POST /v1/runs/{id}/cancel", s.handleCancel)

	// Production agentic episodes, live SSE stream, steering, resume, packets, rewind, mutation, apply.
	s.mux.HandleFunc("GET /v1/episodes", s.handleListEpisodes)
	s.mux.HandleFunc("POST /v1/episodes", s.handleStartEpisode)
	s.mux.HandleFunc("GET /v1/episodes/{id}", s.handleGetEpisode)
	s.mux.HandleFunc("GET /v1/episodes/{id}/events", s.handleEpisodeEvents)
	s.mux.HandleFunc("POST /v1/episodes/{id}/steer", s.handleEpisodeSteer)
	s.mux.HandleFunc("POST /v1/episodes/{id}/resume", s.handleEpisodeResume)
	s.mux.HandleFunc("GET /v1/episodes/{id}/packet", s.handleEpisodePacket)
	s.mux.HandleFunc("GET /v1/episodes/{id}/diff", s.handleEpisodeDiff)
	s.mux.HandleFunc("POST /v1/episodes/{id}/apply", s.handleEpisodeApply)
	s.mux.HandleFunc("POST /v1/episodes/{id}/rewind", s.handleEpisodeRewind)
	s.mux.HandleFunc("POST /v1/episodes/{id}/mutate", s.handleEpisodeMutate)
	s.mux.HandleFunc("POST /v1/episodes/{id}/notify", s.handleEpisodeNotify)

	// External team integrations (Jira, Linear, GitLab, GitHub, Slack/Discord/Teams, Webhooks).
	s.mux.HandleFunc("GET /v1/integrations", s.handleIntegrationsStatus)
	s.mux.HandleFunc("POST /v1/tickets/fetch", s.handleTicketFetch)
	s.mux.HandleFunc("POST /v1/tickets/run", s.handleTicketRun)
	s.mux.HandleFunc("POST /v1/webhooks/{provider}", s.handleWebhook)
}

// ServeHTTP enforces optional CORS, constant-time Bearer token auth, and delegated webhook signatures.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.allowCORS {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	publicPath := r.URL.Path == "/" || r.URL.Path == "/index.html" || r.URL.Path == "/app.js" || r.URL.Path == "/health" || r.URL.Path == "/v1/openapi.json"
	isWebhook := strings.HasPrefix(r.URL.Path, "/v1/webhooks/")
	if s.authToken != "" && !publicPath && !isWebhook {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.authToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "fabric", "version": "2.0.0-dev"})
}

func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(openAPISpec))
}

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	raw, err := s.client.Doctor(r.Context())
	writeRawOrError(w, raw, err)
}

func (s *Server) handleGraph(w http.ResponseWriter, _ *http.Request) {
	g, err := codegraph.Build(s.client.Root)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) handleGraphImpact(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	if target == "" {
		writeError(w, http.StatusBadRequest, "target query parameter is required")
		return
	}
	depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
	g, err := codegraph.Build(s.client.Root)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g.Impact(target, depth))
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	raw, err := s.client.Status(r.Context())
	writeRawOrError(w, raw, err)
}

func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		fabric.RunOptions
		Async bool `json:"async"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Objective) == "" {
		writeError(w, http.StatusBadRequest, "objective is required")
		return
	}
	if !body.Async {
		raw, err := s.client.RunAutonomous(r.Context(), body.RunOptions)
		writeRawOrError(w, raw, err)
		return
	}
	job, ok := s.launchAsync("", func() (json.RawMessage, error) {
		return s.client.RunAutonomous(context.Background(), body.RunOptions)
	})
	if !ok {
		writeError(w, http.StatusTooManyRequests, "too many concurrent async jobs")
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) launchAsync(runID string, fn func() (json.RawMessage, error)) (AsyncJob, bool) {
	select {
	case s.sem <- struct{}{}:
	default:
		return AsyncJob{}, false
	}

	jobID := randomHex(12)
	job := &AsyncJob{JobID: jobID, RunID: runID, Status: "running", StartedAt: time.Now().UTC()}
	s.mu.Lock()
	s.jobs[jobID] = job
	s.jobOrder = append(s.jobOrder, jobID)
	s.evictCompletedJobsLocked()
	snap := *job
	s.mu.Unlock()

	go func() {
		defer func() { <-s.sem }()
		raw, err := fn()
		now := time.Now().UTC()
		s.mu.Lock()
		defer s.mu.Unlock()
		job.EndedAt = &now
		if err != nil {
			job.Status = "failed"
			job.Error = err.Error()
		} else {
			job.Status = "completed"
			job.Result = append(json.RawMessage(nil), raw...)
		}
	}()
	return snap, true
}

func (s *Server) evictCompletedJobsLocked() {
	if len(s.jobs) <= maxStoredJobs {
		return
	}
	kept := s.jobOrder[:0]
	for _, id := range s.jobOrder {
		j, ok := s.jobs[id]
		if !ok {
			continue
		}
		if len(s.jobs) > maxStoredJobs && j.Status != "running" {
			delete(s.jobs, id)
			continue
		}
		kept = append(kept, id)
	}
	s.jobOrder = kept
}

// handleGetJob copies the job snapshot under RLock so JSON encoding never races
// with background worker mutations.
func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("jobId")
	s.mu.RLock()
	job, ok := s.jobs[id]
	var snap AsyncJob
	if ok {
		snap = *job
		if job.EndedAt != nil {
			t := *job.EndedAt
			snap.EndedAt = &t
		}
		if len(job.Result) > 0 {
			snap.Result = append(json.RawMessage(nil), job.Result...)
		}
	}
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleInspectRun(w http.ResponseWriter, r *http.Request) {
	raw, err := s.client.Inspect(r.Context(), r.PathValue("id"))
	writeRawOrError(w, raw, err)
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	raw, err := s.client.Diff(r.Context(), r.PathValue("id"))
	writeRawOrError(w, raw, err)
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	raw, err := s.client.Usage(r.Context(), r.PathValue("id"))
	writeRawOrError(w, raw, err)
}

func (s *Server) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	raw, err := s.client.Checkpoint(r.Context(), r.PathValue("id"))
	writeRawOrError(w, raw, err)
}

func (s *Server) handleDiagnose(w http.ResponseWriter, r *http.Request) {
	raw, err := s.client.Diagnose(r.Context(), r.PathValue("id"))
	writeRawOrError(w, raw, err)
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PlanID string `json:"plan_id"`
		Actor  string `json:"actor"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, err := s.client.Approve(r.Context(), r.PathValue("id"), body.PlanID, body.Actor)
	writeRawOrError(w, raw, err)
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Actor string `json:"actor"`
		Nonce string `json:"nonce"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Nonce == "" {
		body.Nonce = randomHex(8)
	}
	raw, err := s.client.Pause(r.Context(), r.PathValue("id"), body.Actor, body.Nonce)
	writeRawOrError(w, raw, err)
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Autonomous bool `json:"autonomous"`
	}
	_ = decodeBody(w, r, &body)
	raw, err := s.client.Resume(r.Context(), r.PathValue("id"), body.Autonomous)
	writeRawOrError(w, raw, err)
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Actor string `json:"actor"`
		Nonce string `json:"nonce"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Nonce == "" {
		body.Nonce = randomHex(8)
	}
	raw, err := s.client.Cancel(r.Context(), r.PathValue("id"), body.Actor, body.Nonce)
	writeRawOrError(w, raw, err)
}

func (s *Server) handleStreamEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	runID := r.PathValue("id")
	follow := r.URL.Query().Get("follow") == "true"

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	sent := 0
	emit := func() error {
		data, err := s.client.ExportEvents(r.Context(), runID)
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		idx := 0
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			idx++
			if idx <= sent {
				continue
			}
			fmt.Fprintf(w, "id: %d\nevent: journal\ndata: %s\n\n", idx, line)
			sent = idx
		}
		flusher.Flush()
		return nil
	}

	if err := emit(); err != nil {
		fmt.Fprintf(w, "event: error\ndata: %q\n\n", err.Error())
		flusher.Flush()
		return
	}
	if !follow {
		return
	}
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_ = emit()
		}
	}
}

func (s *Server) handleListEpisodes(w http.ResponseWriter, _ *http.Request) {
	eps, err := agentloop.ListEpisodes(s.client.Root)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sort.Slice(eps, func(i, j int) bool { return eps[i].StartedAt.After(eps[j].StartedAt) })
	writeJSON(w, http.StatusOK, eps)
}

func (s *Server) handleStartEpisode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Objective     string   `json:"objective"`
		Dialect       string   `json:"dialect"`
		BaseURL       string   `json:"base_url"`
		APIKeyEnv     string   `json:"api_key_env"`
		Model         string   `json:"model"`
		MaxUSD        float64  `json:"max_usd"`
		MaxTurns      int      `json:"max_turns"`
		AllowCommand  bool     `json:"allow_command"`
		MutationAudit bool     `json:"mutation_audit"`
		AllowedWrite  []string `json:"allowed_write"`
		Async         bool     `json:"async"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Objective) == "" {
		writeError(w, http.StatusBadRequest, "objective is required")
		return
	}
	if math.IsNaN(body.MaxUSD) || math.IsInf(body.MaxUSD, 0) {
		writeError(w, http.StatusBadRequest, "invalid max_usd value")
		return
	}
	budget := cost.Budget{MaxUSD: body.MaxUSD, MaxTurns: body.MaxTurns}
	if err := budget.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var repoCfg *config.Config
	if loaded, err := agentloop.LoadRepoConfig(s.client.Root); err == nil {
		repoCfg = &loaded
	}
	model, err := agentloop.ModelFromConfig(repoCfg, agentloop.ModelOverrides{
		Dialect:   body.Dialect,
		BaseURL:   body.BaseURL,
		APIKeyEnv: body.APIKeyEnv,
		Model:     body.Model,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	runID := randomHex(16)
	stream := newEpisodeStream()
	steerCh := make(chan string, 16)
	s.mu.Lock()
	s.epStreams[runID] = stream
	s.epSteer[runID] = steerCh
	s.mu.Unlock()

	cleanupLive := func() {
		stream.finish()
		s.mu.Lock()
		delete(s.epSteer, runID)
		s.mu.Unlock()
	}

	loopCfg := agentloop.Config{
		RunID:         runID,
		RepoRoot:      s.client.Root,
		Objective:     body.Objective,
		Model:         model,
		AllowedWrite:  body.AllowedWrite,
		MaxTurns:      body.MaxTurns,
		MutationAudit: body.MutationAudit,
		AllowCommand:  body.AllowCommand,
		Budget:        budget,
		SteerCh:       steerCh,
		OnEvent:       stream.publish,
	}
	if repoCfg != nil {
		loopCfg.Verification = repoCfg.Verification
		loopCfg.Hooks = repoCfg.Hooks
		if repoCfg.Budget != nil {
			if loopCfg.Budget.MaxUSD == 0 {
				loopCfg.Budget.MaxUSD = repoCfg.Budget.MaxUSD
			}
			if loopCfg.Budget.MaxTokens == 0 {
				loopCfg.Budget.MaxTokens = repoCfg.Budget.MaxTokens
			}
			if loopCfg.Budget.MaxWallSeconds == 0 {
				loopCfg.Budget.MaxWallSeconds = repoCfg.Budget.MaxWallSeconds
			}
		}
		if repoCfg.Sandbox != nil {
			loopCfg.Sandbox = *repoCfg.Sandbox
		}
	}

	if !body.Async {
		defer cleanupLive()
		ep, runErr := agentloop.Run(r.Context(), loopCfg)
		if runErr != nil {
			writeError(w, http.StatusBadRequest, runErr.Error())
			return
		}
		writeJSON(w, http.StatusOK, ep)
		return
	}

	job, ok := s.launchAsync(runID, func() (json.RawMessage, error) {
		defer cleanupLive()
		ep, runErr := agentloop.Run(context.Background(), loopCfg)
		if runErr != nil {
			return nil, runErr
		}
		b, err := json.Marshal(ep)
		return json.RawMessage(b), err
	})
	if !ok {
		cleanupLive()
		writeError(w, http.StatusTooManyRequests, "too many concurrent async jobs")
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleEpisodeSteer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Message string `json:"message"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	msg := strings.TrimSpace(body.Message)
	if msg == "" {
		writeError(w, http.StatusBadRequest, "steering message is required")
		return
	}
	s.mu.RLock()
	ch := s.epSteer[id]
	s.mu.RUnlock()
	if ch == nil {
		writeError(w, http.StatusConflict, "episode is not currently running")
		return
	}
	select {
	case ch <- msg:
		writeJSON(w, http.StatusOK, map[string]string{"status": "queued", "run_id": id})
	default:
		writeError(w, http.StatusTooManyRequests, "steering queue full for episode")
	}
}

func (s *Server) handleEpisodeResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, err := agentloop.LoadEpisodeState(s.client.Root, id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		Dialect  string  `json:"dialect"`
		BaseURL  string  `json:"base_url"`
		Model    string  `json:"model"`
		MaxUSD   float64 `json:"max_usd"`
		MaxTurns int     `json:"max_turns"`
		Async    bool    `json:"async"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	budget := cost.Budget{MaxUSD: body.MaxUSD, MaxTurns: body.MaxTurns}
	if math.IsNaN(body.MaxUSD) || math.IsInf(body.MaxUSD, 0) || budget.Validate() != nil {
		writeError(w, http.StatusBadRequest, "invalid budget override")
		return
	}
	var repoCfg *config.Config
	if loaded, cfgErr := agentloop.LoadRepoConfig(s.client.Root); cfgErr == nil {
		repoCfg = &loaded
	}
	mdlName := strings.TrimSpace(body.Model)
	if mdlName == "" {
		mdlName = st.Episode.Model
	}
	model, err := agentloop.ModelFromConfig(repoCfg, agentloop.ModelOverrides{
		Dialect: body.Dialect,
		BaseURL: body.BaseURL,
		Model:   mdlName,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	stream := newEpisodeStream()
	steerCh := make(chan string, 16)
	s.mu.Lock()
	s.epStreams[id] = stream
	s.epSteer[id] = steerCh
	s.mu.Unlock()
	cleanupLive := func() {
		stream.finish()
		s.mu.Lock()
		delete(s.epSteer, id)
		s.mu.Unlock()
	}

	loopCfg := agentloop.Config{
		RepoRoot:     s.client.Root,
		ResumeRunID:  id,
		Model:        model,
		MaxTurns:     body.MaxTurns,
		Budget:       budget,
		AllowCommand: true,
		SteerCh:      steerCh,
		OnEvent:      stream.publish,
	}
	if repoCfg != nil {
		loopCfg.Verification = repoCfg.Verification
		loopCfg.Hooks = repoCfg.Hooks
		if repoCfg.Sandbox != nil {
			loopCfg.Sandbox = *repoCfg.Sandbox
		}
	}

	if !body.Async {
		defer cleanupLive()
		ep, runErr := agentloop.Run(r.Context(), loopCfg)
		if runErr != nil {
			writeError(w, http.StatusBadRequest, runErr.Error())
			return
		}
		writeJSON(w, http.StatusOK, ep)
		return
	}
	job, ok := s.launchAsync(id, func() (json.RawMessage, error) {
		defer cleanupLive()
		ep, runErr := agentloop.Run(context.Background(), loopCfg)
		if runErr != nil {
			return nil, runErr
		}
		b, err := json.Marshal(ep)
		return json.RawMessage(b), err
	})
	if !ok {
		cleanupLive()
		writeError(w, http.StatusTooManyRequests, "too many concurrent async jobs")
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleEpisodeEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	id := r.PathValue("id")
	follow := r.URL.Query().Get("follow") == "true"

	s.mu.RLock()
	stream := s.epStreams[id]
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	emitEv := func(seq int, ev agentloop.Event) {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", seq, ev.Kind, b)
		flusher.Flush()
	}

	if stream != nil {
		hist, ch, closed := stream.subscribe()
		seq := 0
		for _, ev := range hist {
			seq++
			emitEv(seq, ev)
		}
		if closed || !follow || ch == nil {
			if ch != nil {
				stream.unsubscribe(ch)
			}
			return
		}
		defer stream.unsubscribe(ch)
		keepalive := time.NewTicker(15 * time.Second)
		defer keepalive.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-keepalive.C:
				fmt.Fprint(w, ": keepalive\n\n")
				flusher.Flush()
			case ev, open := <-ch:
				if !open {
					return
				}
				seq++
				emitEv(seq, ev)
			}
		}
	}

	// Fallback to persisted episode events.
	ep, err := agentloop.LoadEpisode(s.client.Root, id)
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %q\n\n", err.Error())
		flusher.Flush()
		return
	}
	for i, ev := range ep.Events {
		emitEv(i+1, ev)
	}
}

func (s *Server) handleGetEpisode(w http.ResponseWriter, r *http.Request) {
	ep, err := agentloop.LoadEpisode(s.client.Root, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ep)
}

func (s *Server) handleEpisodePacket(w http.ResponseWriter, r *http.Request) {
	ep, err := agentloop.LoadEpisode(s.client.Root, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	pkt := packet.FromEpisode(ep)
	if strings.EqualFold(r.URL.Query().Get("format"), "markdown") {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pkt.Markdown()))
		return
	}
	writeJSON(w, http.StatusOK, pkt)
}

func (s *Server) handleEpisodeDiff(w http.ResponseWriter, r *http.Request) {
	ep, err := agentloop.LoadEpisode(s.client.Root, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	diffBytes, err := packet.WorktreePatch(r.Context(), ep.WorkDir, "HEAD", false, 5<<20)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("worktree diff failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": ep.RunID, "work_dir": ep.WorkDir, "diff": string(diffBytes)})
}

func (s *Server) handleEpisodeApply(w http.ResponseWriter, r *http.Request) {
	ep, err := agentloop.LoadEpisode(s.client.Root, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		Branch string `json:"branch"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := packet.ApplyWorktreePatch(r.Context(), s.client.Root, ep.WorkDir, strings.TrimSpace(body.Branch)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "applied", "run_id": ep.RunID, "branch": body.Branch})
}

func (s *Server) handleEpisodeRewind(w http.ResponseWriter, r *http.Request) {
	ep, err := agentloop.LoadEpisode(s.client.Root, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		Step int `json:"step"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snap, err := rewind.Restore(r.Context(), ep.WorkDir, ep.RunID, body.Step)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "rewound", "run_id": ep.RunID, "snapshot": snap})
}

func (s *Server) handleEpisodeMutate(w http.ResponseWriter, r *http.Request) {
	ep, err := agentloop.LoadEpisode(s.client.Root, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		MaxMutants int      `json:"max_mutants"`
		TestArgv   []string `json:"test_argv"`
	}
	_ = decodeBody(w, r, &body)
	argv := body.TestArgv
	sb := sandbox.Config{}
	if len(argv) == 0 {
		if cfg, cfgErr := agentloop.LoadRepoConfig(s.client.Root); cfgErr == nil {
			if len(cfg.Verification) > 0 {
				argv = cfg.Verification[0].Argv
			}
			if cfg.Sandbox != nil {
				sb = *cfg.Sandbox
			}
		}
	}
	if len(argv) == 0 {
		writeError(w, http.StatusBadRequest, "no verification command configured; provide test_argv")
		return
	}
	rep, err := mutation.AuditDiff(r.Context(), mutation.Config{
		WorkDir:    ep.WorkDir,
		TestArgv:   argv,
		Sandbox:    sb,
		MaxMutants: body.MaxMutants,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ep.Mutation = &rep
	_ = agentloop.SaveEpisode(s.client.Root, ep)
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) handleIntegrationsStatus(w http.ResponseWriter, _ *http.Request) {
	var intCfg *integrations.Config
	if cfg, err := agentloop.LoadRepoConfig(s.client.Root); err == nil {
		intCfg = cfg.Integrations
	}
	writeJSON(w, http.StatusOK, integrations.InspectStatus(intCfg))
}

func (s *Server) handleTicketFetch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref      string `json:"ref"`
		Provider string `json:"provider"`
		Project  string `json:"project"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Ref) == "" {
		writeError(w, http.StatusBadRequest, "ticket ref is required")
		return
	}
	var intCfg *integrations.Config
	if cfg, err := agentloop.LoadRepoConfig(s.client.Root); err == nil {
		intCfg = cfg.Integrations
	}
	tr, key, err := integrations.ResolveTrackerForRef(intCfg, body.Ref, body.Provider, body.Project, s.client.Root, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tk, err := tr.FetchTicket(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tk)
}

func (s *Server) handleEpisodeNotify(w http.ResponseWriter, r *http.Request) {
	ep, err := agentloop.LoadEpisode(s.client.Root, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		Channel   string `json:"channel"`
		TicketKey string `json:"ticket_key"`
		TicketURL string `json:"ticket_url"`
		PRURL     string `json:"pr_url"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var notifyCfg *integrations.NotifyConfig
	if cfg, cfgErr := agentloop.LoadRepoConfig(s.client.Root); cfgErr == nil && cfg.Integrations != nil {
		notifyCfg = cfg.Integrations.Notify
	}
	res := integrations.NotifyAll(r.Context(), notifyCfg, packet.NotificationFromEpisode(ep, body.TicketKey, body.TicketURL, body.PRURL), body.Channel, nil)
	writeJSON(w, http.StatusOK, map[string]any{"run_id": ep.RunID, "deliveries": res})
}

type ticketLaunchOpts struct {
	Objective     string
	Dialect       string
	BaseURL       string
	Model         string
	MaxUSD        float64
	MaxTurns      int
	MutationAudit bool
	AutoComment   bool
	AutoNotify    bool
	TransitionTo  string
	Ticket        *integrations.Ticket
	Tracker       integrations.Tracker
}

func (s *Server) launchTicketEpisode(ctx context.Context, async bool, opts ticketLaunchOpts) (*AsyncJob, agentloop.Episode, int, error) {
	budget := cost.Budget{MaxUSD: opts.MaxUSD, MaxTurns: opts.MaxTurns}
	if math.IsNaN(opts.MaxUSD) || math.IsInf(opts.MaxUSD, 0) || budget.Validate() != nil {
		return nil, agentloop.Episode{}, http.StatusBadRequest, fmt.Errorf("invalid budget parameters")
	}
	var repoCfg *config.Config
	if loaded, cfgErr := agentloop.LoadRepoConfig(s.client.Root); cfgErr == nil {
		repoCfg = &loaded
	}
	model, err := agentloop.ModelFromConfig(repoCfg, agentloop.ModelOverrides{
		Dialect: opts.Dialect,
		BaseURL: opts.BaseURL,
		Model:   opts.Model,
	})
	if err != nil {
		return nil, agentloop.Episode{}, http.StatusBadRequest, err
	}
	runID := randomHex(16)
	stream := newEpisodeStream()
	steerCh := make(chan string, 16)
	s.mu.Lock()
	s.epStreams[runID] = stream
	s.epSteer[runID] = steerCh
	s.mu.Unlock()
	cleanupLive := func() {
		stream.finish()
		s.mu.Lock()
		delete(s.epSteer, runID)
		s.mu.Unlock()
	}

	maxTurns := opts.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 24
	}
	loopCfg := agentloop.Config{
		RunID:         runID,
		RepoRoot:      s.client.Root,
		Objective:     opts.Objective,
		Model:         model,
		MaxTurns:      maxTurns,
		MutationAudit: opts.MutationAudit,
		AllowCommand:  true,
		Budget:        budget,
		SteerCh:       steerCh,
		OnEvent:       stream.publish,
	}
	if repoCfg != nil {
		loopCfg.Verification = repoCfg.Verification
		loopCfg.Hooks = repoCfg.Hooks
		if repoCfg.Sandbox != nil {
			loopCfg.Sandbox = *repoCfg.Sandbox
		}
	}

	finalize := func(runCtx context.Context, ep agentloop.Episode) {
		if opts.Ticket != nil && opts.Tracker != nil {
			if opts.AutoComment {
				_ = opts.Tracker.PostComment(runCtx, opts.Ticket.Key, packet.FromEpisode(ep).Markdown())
			}
			if ep.Status == "READY" && strings.TrimSpace(opts.TransitionTo) != "" {
				_ = opts.Tracker.Transition(runCtx, opts.Ticket.Key, opts.TransitionTo)
			}
		}
		if opts.AutoNotify && repoCfg != nil && repoCfg.Integrations != nil {
			key, turl := "", ""
			if opts.Ticket != nil {
				key, turl = opts.Ticket.Key, opts.Ticket.URL
			}
			_ = integrations.NotifyAll(runCtx, repoCfg.Integrations.Notify, packet.NotificationFromEpisode(ep, key, turl, ""), "all", nil)
		}
	}

	if !async {
		defer cleanupLive()
		ep, runErr := agentloop.Run(ctx, loopCfg)
		if runErr != nil {
			return nil, agentloop.Episode{}, http.StatusBadRequest, runErr
		}
		finalize(ctx, ep)
		return nil, ep, http.StatusOK, nil
	}

	job, ok := s.launchAsync(runID, func() (json.RawMessage, error) {
		defer cleanupLive()
		bgCtx := context.WithoutCancel(ctx)
		ep, runErr := agentloop.Run(bgCtx, loopCfg)
		if runErr != nil {
			return nil, runErr
		}
		finalize(bgCtx, ep)
		b, err := json.Marshal(ep)
		return json.RawMessage(b), err
	})
	if !ok {
		cleanupLive()
		return nil, agentloop.Episode{}, http.StatusTooManyRequests, fmt.Errorf("too many concurrent async jobs")
	}
	return &job, agentloop.Episode{}, http.StatusAccepted, nil
}

func (s *Server) handleTicketRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref           string  `json:"ref"`
		Provider      string  `json:"provider"`
		Project       string  `json:"project"`
		Dialect       string  `json:"dialect"`
		BaseURL       string  `json:"base_url"`
		Model         string  `json:"model"`
		MaxUSD        float64 `json:"max_usd"`
		MaxTurns      int     `json:"max_turns"`
		MutationAudit bool    `json:"mutation_audit"`
		Comment       bool    `json:"comment"`
		Transition    string  `json:"transition"`
		Notify        bool    `json:"notify"`
		Async         bool    `json:"async"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Ref) == "" {
		writeError(w, http.StatusBadRequest, "ticket ref is required")
		return
	}
	var intCfg *integrations.Config
	if cfg, err := agentloop.LoadRepoConfig(s.client.Root); err == nil {
		intCfg = cfg.Integrations
	}
	tr, key, err := integrations.ResolveTrackerForRef(intCfg, body.Ref, body.Provider, body.Project, s.client.Root, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tk, err := tr.FetchTicket(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	job, ep, code, launchErr := s.launchTicketEpisode(r.Context(), body.Async, ticketLaunchOpts{
		Objective:     integrations.BuildObjective(tk),
		Dialect:       body.Dialect,
		BaseURL:       body.BaseURL,
		Model:         body.Model,
		MaxUSD:        body.MaxUSD,
		MaxTurns:      body.MaxTurns,
		MutationAudit: body.MutationAudit,
		AutoComment:   body.Comment,
		AutoNotify:    body.Notify,
		TransitionTo:  body.Transition,
		Ticket:        &tk,
		Tracker:       tr,
	})
	if launchErr != nil {
		writeError(w, code, launchErr.Error())
		return
	}
	if job != nil {
		writeJSON(w, code, map[string]any{"job": job, "ticket": tk})
		return
	}
	writeJSON(w, code, map[string]any{"episode": ep, "ticket": tk})
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var repoCfg *config.Config
	var whCfg *integrations.WebhookConfig
	var intCfg *integrations.Config
	if loaded, cfgErr := agentloop.LoadRepoConfig(s.client.Root); cfgErr == nil {
		repoCfg = &loaded
		intCfg = repoCfg.Integrations
		if intCfg != nil {
			whCfg = intCfg.Webhooks
		}
	}
	tr, parseErr := integrations.ParseInboundWebhook(provider, r.Header, raw, whCfg)
	if parseErr != nil {
		switch parseErr {
		case integrations.ErrWebhookNotConfigured:
			writeError(w, http.StatusServiceUnavailable, parseErr.Error())
		case integrations.ErrWebhookUnauthorized:
			writeError(w, http.StatusUnauthorized, parseErr.Error())
		default:
			writeError(w, http.StatusBadRequest, parseErr.Error())
		}
		return
	}
	if tr.Ignored {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "reason": tr.IgnoreReason, "provider": tr.Provider})
		return
	}
	var tracker integrations.Tracker
	var tk *integrations.Ticket
	if tr.TicketKey != "" {
		if resolved, _, tErr := integrations.ResolveTrackerForRef(intCfg, tr.TicketKey, tr.Provider, "", s.client.Root, nil); tErr == nil {
			tracker = resolved
			tk = &integrations.Ticket{Provider: tr.Provider, Key: tr.TicketKey, Title: tr.Title, URL: tr.URL}
		}
	}
	job, _, code, launchErr := s.launchTicketEpisode(r.Context(), true, ticketLaunchOpts{
		Objective:   tr.Objective(),
		MaxUSD:      whCfg.DefaultMaxUSD,
		MaxTurns:    whCfg.DefaultMaxTurns,
		AutoComment: whCfg.AutoComment,
		AutoNotify:  whCfg.AutoNotify,
		Ticket:      tk,
		Tracker:     tracker,
	})
	if launchErr != nil {
		writeError(w, code, launchErr.Error())
		return
	}
	writeJSON(w, code, map[string]any{"status": "accepted", "job": job, "trigger": tr})
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func writeRawOrError(w http.ResponseWriter, raw json.RawMessage, err error) {
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
	_, _ = w.Write([]byte("\n"))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

const openAPISpec = `{
  "openapi": "3.1.0",
  "info": {
    "title": "Fabric Autonomous Engineering Runtime API",
    "version": "2.0.0-dev",
    "description": "Local HTTP/JSON + SSE API, Code Knowledge Graph, and Mission Control backend for orchestrating, inspecting, rewinding, auditing, and applying Fabric runs and agentic episodes."
  },
  "paths": {
    "/": {"get": {"summary": "Embedded Mission Control Web Dashboard"}},
    "/health": {"get": {"summary": "Health check"}},
    "/v1/doctor": {"get": {"summary": "Validate configuration, runtimes, hooks, budget, sandbox, and verification readiness"}},
    "/v1/graph": {"get": {"summary": "Build and return local Code Knowledge Graph (nodes, edges, clusters, PageRank hubs)"}},
    "/v1/graph/impact": {"get": {"summary": "Compute multi-hop blast radius (?target=<symbol|path>&depth=2)"}},
    "/v1/runs": {
      "get": {"summary": "List classic replay-validated runs"},
      "post": {"summary": "Start an autonomous classic run (sync or async)"}
    },
    "/v1/jobs/{jobId}": {"get": {"summary": "Check status of an async run or episode job"}},
    "/v1/runs/{id}": {"get": {"summary": "Inspect full replay-validated run snapshot"}},
    "/v1/runs/{id}/events": {"get": {"summary": "Stream canonical journal events via SSE (?follow=true)"}},
    "/v1/runs/{id}/diff": {"get": {"summary": "Get isolated candidate diff"}},
    "/v1/runs/{id}/usage": {"get": {"summary": "Get replay-validated token usage"}},
    "/v1/runs/{id}/checkpoint": {"get": {"summary": "Get payload-free run checkpoint"}},
    "/v1/runs/{id}/diagnose": {"get": {"summary": "Get structured repair diagnosis and admitted scope"}},
    "/v1/runs/{id}/approve": {"post": {"summary": "Approve a plan with an explicit actor"}},
    "/v1/runs/{id}/pause": {"post": {"summary": "Request durable pause"}},
    "/v1/runs/{id}/resume": {"post": {"summary": "Resume a run"}},
    "/v1/runs/{id}/cancel": {"post": {"summary": "Request durable cancellation"}},
    "/v1/episodes": {
      "get": {"summary": "List production agentic loop episodes"},
      "post": {"summary": "Launch a bounded isolated agentic episode (sync or async)"}
    },
    "/v1/episodes/{id}": {"get": {"summary": "Inspect a persisted agentic episode"}},
    "/v1/episodes/{id}/events": {"get": {"summary": "Stream real-time agentic episode turns and tool calls via SSE (?follow=true)"}},
    "/v1/episodes/{id}/steer": {"post": {"summary": "Send a live steering instruction to a running agentic episode"}},
    "/v1/episodes/{id}/resume": {"post": {"summary": "Resume an interrupted or budget-stopped episode with updated limits"}},
    "/v1/episodes/{id}/packet": {"get": {"summary": "Get Structured Review Packet (JSON or ?format=markdown)"}},
    "/v1/episodes/{id}/diff": {"get": {"summary": "Get complete unified Git diff of episode worktree (including new files)"}},
    "/v1/episodes/{id}/apply": {"post": {"summary": "Safely validate and apply episode worktree patch to repository"}},
    "/v1/episodes/{id}/rewind": {"post": {"summary": "Rewind episode worktree to a lightweight Git checkpoint step"}},
    "/v1/episodes/{id}/mutate": {"post": {"summary": "Run diff-scoped mutation audit against episode worktree"}},
    "/v1/episodes/{id}/notify": {"post": {"summary": "Dispatch an episode Review Packet to Slack, Discord, Microsoft Teams, or signed webhook"}},
    "/v1/integrations": {"get": {"summary": "Inspect configured trackers, chat notification channels, and webhook readiness (no secrets)"}},
    "/v1/tickets/fetch": {"post": {"summary": "Fetch a normalized ticket from Jira, Linear, GitLab, or GitHub"}},
    "/v1/tickets/run": {"post": {"summary": "Fetch an external ticket and launch a bounded isolated agentic episode (sync or async)"}},
    "/v1/webhooks/{provider}": {"post": {"summary": "Receive a verified inbound webhook trigger (jira, linear, github, generic; fail-closed signature check)"}}
  }
}
`
