package dashboard

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/forge/forge/internal/artifacts"
	"github.com/forge/forge/internal/db"
)

type Server struct {
	db        *db.DB
	port      int
	server    *http.Server
	artifacts artifacts.Store
}

func New(database *db.DB, port int) *Server {
	return &Server{
		db: database, port: port,
		artifacts: artifacts.Store{DB: database, Root: filepath.Join(filepath.Dir(database.Path), "artifacts")},
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/traces", s.handleTraces)
	mux.HandleFunc("/api/tasks", s.handleEvaluationTask)
	mux.HandleFunc("/api/evaluations", s.handleEvaluation)
	mux.HandleFunc("/api/evaluations/report", s.handleEvaluationReport)
	mux.HandleFunc("/api/artifacts", s.handleArtifacts)
	mux.HandleFunc("/api/artifacts/", s.handleArtifact)
	mux.HandleFunc("/api/principles", s.handlePrinciples)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/conflicts", s.handleConflicts)
	mux.HandleFunc("/api/conflicts/resolve", s.handleResolveConflict)

	s.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	return s.server.ListenAndServe()
}

type artifactUpload struct {
	TraceID   string `json:"trace_id"`
	TaskID    string `json:"task_id,omitempty"`
	Kind      string `json:"kind"`
	MediaType string `json:"media_type"`
	Content   string `json:"content_base64"`
	Metadata  string `json:"metadata,omitempty"`
}

func (s *Server) handleArtifacts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var upload artifactUpload
		r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
		if err := json.NewDecoder(r.Body).Decode(&upload); err != nil {
			http.Error(w, "invalid artifact JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if upload.TraceID == "" || upload.Kind == "" || upload.Content == "" {
			http.Error(w, "trace_id, kind, and content_base64 are required", http.StatusBadRequest)
			return
		}
		content, err := base64.StdEncoding.DecodeString(upload.Content)
		if err != nil {
			http.Error(w, "content_base64 is invalid: "+err.Error(), http.StatusBadRequest)
			return
		}
		artifact, err := s.artifacts.Put(upload.TraceID, upload.TaskID, upload.Kind, upload.MediaType, content, upload.Metadata)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(artifact)
	case http.MethodGet:
		limit := 100
		if l := r.URL.Query().Get("limit"); l != "" {
			if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 1000 {
				limit = parsed
			}
		}
		items, err := s.db.EvaluationArtifacts(r.URL.Query().Get("trace_id"), r.URL.Query().Get("task_id"), limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/artifacts/")
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, "artifact id is required", http.StatusBadRequest)
		return
	}
	artifact, err := s.db.EvaluationArtifactByID(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if artifact == nil {
		http.NotFound(w, r)
		return
	}
	reader, err := s.artifacts.Open(*artifact)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", artifact.MediaType)
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.ByteSize, 10))
	if _, err := io.Copy(w, reader); err != nil {
		return
	}
}

func (s *Server) handleEvaluationTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var task db.EvaluationTask
	if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
		http.Error(w, "invalid task JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if task.ID == "" || task.Prompt == "" {
		http.Error(w, "id and prompt are required", http.StatusBadRequest)
		return
	}
	if err := s.db.UpsertEvaluationTask(&task); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(task)
}

func (s *Server) handleEvaluation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var evaluation db.TraceEvaluation
	if err := json.NewDecoder(r.Body).Decode(&evaluation); err != nil {
		http.Error(w, "invalid evaluation JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if evaluation.TraceID == "" || evaluation.Evaluator == "" {
		http.Error(w, "trace_id and evaluator are required", http.StatusBadRequest)
		return
	}
	if err := s.db.InsertTraceEvaluation(&evaluation); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(evaluation)
}

func (s *Server) handleEvaluationReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	report, err := s.db.EvaluationReport(r.URL.Query().Get("task_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

func (s *Server) handleTraces(w http.ResponseWriter, r *http.Request) {
	traceID := r.URL.Query().Get("trace_id")
	if traceID == "" {
		http.Error(w, "trace_id is required", http.StatusBadRequest)
		return
	}
	limit := 1000
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 10000 {
			limit = parsed
		}
	}
	events, err := s.db.TraceEvents(traceID, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	evaluations, err := s.db.TraceEvaluations(traceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var task *db.EvaluationTask
	if taskID := r.URL.Query().Get("task_id"); taskID != "" {
		task, err = s.db.EvaluationTaskByID(taskID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else if len(events) > 0 && events[0].TaskID != "" {
		task, err = s.db.EvaluationTaskByID(events[0].TaskID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	response := struct {
		TraceID     string               `json:"trace_id"`
		Task        *db.EvaluationTask   `json:"task,omitempty"`
		Events      []db.Event           `json:"events"`
		Evaluations []db.TraceEvaluation `json:"evaluations"`
	}{TraceID: traceID, Task: task, Events: events, Evaluations: evaluations}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
func (s *Server) Stop() error {
	if s.server != nil {
		return s.server.Close()
	}
	return nil
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.New("index").Parse(indexHTML))
	tmpl.Execute(w, struct{ Port int }{s.port})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	events, err := s.db.RecentEvents(limit)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	type EventJSON struct {
		ID             string `json:"id"`
		TS             string `json:"ts"`
		TraceID        string `json:"trace_id"`
		SpanID         string `json:"span_id"`
		ParentSpanID   string `json:"parent_span_id,omitempty"`
		Sequence       int64  `json:"sequence,omitempty"`
		DurationMS     int64  `json:"duration_ms,omitempty"`
		Status         string `json:"status,omitempty"`
		ExitCode       int    `json:"exit_code,omitempty"`
		Model          string `json:"model,omitempty"`
		TaskID         string `json:"task_id,omitempty"`
		CWD            string `json:"cwd,omitempty"`
		GitBranch      string `json:"git_branch,omitempty"`
		GitCommit      string `json:"git_commit,omitempty"`
		Files          string `json:"files,omitempty"`
		TranscriptPath string `json:"transcript_path,omitempty"`
		SessionID      string `json:"session_id"`
		ProjectID      string `json:"project_id"`
		SourceTool     string `json:"source_tool"`
		EventType      string `json:"event_type"`
		ToolName       string `json:"tool_name"`
		Payload        string `json:"payload"`
		Distilled      bool   `json:"distilled"`
	}

	var result []EventJSON
	for _, e := range events {
		payload := e.Payload
		if len(payload) > 200 {
			payload = payload[:200] + "..."
		}
		result = append(result, EventJSON{
			ID:             short(e.ID, 8),
			TS:             e.TS,
			TraceID:        e.TraceID,
			SpanID:         e.SpanID,
			ParentSpanID:   e.ParentSpanID,
			Sequence:       e.Sequence,
			DurationMS:     e.DurationMS,
			Status:         e.Status,
			ExitCode:       e.ExitCode,
			Model:          e.Model,
			TaskID:         e.TaskID,
			CWD:            e.CWD,
			GitBranch:      e.GitBranch,
			GitCommit:      e.GitCommit,
			Files:          e.Files,
			TranscriptPath: e.TranscriptPath,
			SessionID:      e.SessionID,
			ProjectID:      e.ProjectID,
			SourceTool:     e.SourceTool,
			EventType:      e.EventType,
			ToolName:       e.ToolName,
			Payload:        payload,
			Distilled:      e.Distilled,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *Server) handlePrinciples(w http.ResponseWriter, r *http.Request) {
	limit := 1000
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		}
	}

	principles, err := s.db.RecentPrinciples(limit)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	type PrincipleJSON struct {
		ID          string   `json:"id"`
		TS          string   `json:"ts"`
		Type        string   `json:"type"`
		Title       string   `json:"title"`
		Narrative   string   `json:"narrative"`
		ImpactScore float64  `json:"impact_score"`
		ProjectID   string   `json:"project_id"`
		Concepts    []string `json:"concepts"`
	}

	var result []PrincipleJSON
	for _, p := range principles {
		result = append(result, PrincipleJSON{
			ID:          short(p.ID, 8),
			TS:          p.TS,
			Type:        p.Type,
			Title:       p.Title,
			Narrative:   p.Narrative,
			ImpactScore: p.ImpactScore,
			ProjectID:   p.ProjectID,
			Concepts:    p.Concepts,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	total, undistilled, _ := s.db.EventCount()
	principles, _ := s.db.PrincipleCount()
	sessions, _ := s.db.SessionSummaryCount()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"total":       total,
		"undistilled": undistilled,
		"principles":  principles,
		"sessions":    sessions,
	})
}

type conflictPairJSON struct {
	A principleJSON `json:"a"`
	B principleJSON `json:"b"`
}

type principleJSON struct {
	ID          string   `json:"id"`
	TS          string   `json:"ts"`
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Narrative   string   `json:"narrative"`
	ImpactScore float64  `json:"impact_score"`
	ProjectID   string   `json:"project_id"`
	Concepts    []string `json:"concepts"`
	Status      string   `json:"status"`
}

func toPrincipleJSON(p db.Principle) principleJSON {
	return principleJSON{
		ID:          p.ID,
		TS:          p.TS,
		Type:        p.Type,
		Title:       p.Title,
		Narrative:   p.Narrative,
		ImpactScore: p.ImpactScore,
		ProjectID:   p.ProjectID,
		Concepts:    p.Concepts,
		Status:      p.Status,
	}
}

func (s *Server) handleConflicts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	conflicts, err := s.db.ConflictingPrinciples(50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	seen := make(map[string]bool)
	var pairs []conflictPairJSON
	for _, p := range conflicts {
		peerID := p.ConflictPeerID
		if peerID == "" {
			continue
		}
		// Deduplication key — canonical order by ID.
		lo, hi := p.ID, peerID
		if lo > hi {
			lo, hi = hi, lo
		}
		key := lo + ":" + hi
		if seen[key] {
			continue
		}

		peer, err := s.db.GetPrincipleByID(peerID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if peer == nil {
			// Peer deleted externally — repair orphan and skip.
			_ = s.db.ClearConflictStatus(p.ID)
			continue
		}

		seen[key] = true
		pairs = append(pairs, conflictPairJSON{
			A: toPrincipleJSON(p),
			B: toPrincipleJSON(*peer),
		})
	}

	if pairs == nil {
		pairs = []conflictPairJSON{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pairs)
}

func (s *Server) handleResolveConflict(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		KeepID   string `json:"keep_id"`
		DeleteID string `json:"delete_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.KeepID == "" || body.DeleteID == "" {
		http.Error(w, "invalid request body: keep_id and delete_id required", http.StatusBadRequest)
		return
	}
	if err := s.db.ResolvePrincipleConflict(body.KeepID, body.DeleteID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Forge Memory Dashboard</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body { font-family: Inter, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #f5f7fa; color: #1b2437; min-height: 100vh; -webkit-font-smoothing: antialiased; }
        .container { max-width: 1200px; margin: 0 auto; padding: 24px; }
        header { display: flex; align-items: center; justify-content: space-between; gap: 24px; padding: 20px 0; border-bottom: 1px solid #e8ebf2; flex-wrap: wrap; }
        .brand { display: flex; align-items: center; gap: 12px; }
        .logo { width: 36px; height: 36px; border-radius: 10px; background: #6366f1; color: #fff; display: flex; align-items: center; justify-content: center; }
        .logo .ic { width: 20px; height: 20px; }
        h1 { font-size: 18px; font-weight: 700; color: #1b2437; letter-spacing: -0.01em; }
        .ic { width: 15px; height: 15px; fill: none; stroke: currentColor; stroke-width: 2; stroke-linecap: round; stroke-linejoin: round; vertical-align: -2px; }
        .stats { display: flex; gap: 12px; }
        .stat { background: #fff; border: 1px solid #e8ebf2; padding: 10px 16px; border-radius: 12px; text-align: center; box-shadow: 0 1px 2px rgba(22,34,55,.04); min-width: 82px; }
        .stat-value { font-size: 20px; font-weight: 700; color: #6366f1; }
        .stat-label { font-size: 11px; color: #98a2b4; margin-top: 4px; }
        .tabs { display: flex; gap: 6px; margin: 22px 0; align-items: center; flex-wrap: wrap; }
        .tab { display: inline-flex; align-items: center; gap: 7px; padding: 8px 16px; background: transparent; border: 1px solid transparent; color: #5d6a7f; cursor: pointer; border-radius: 999px; font-size: 13px; }
        .tab:hover { background: #fff; color: #1b2437; }
        .tab.active { background: #6366f1; color: #fff; }
        .panel { display: none; }
        .panel.active { display: block; }
        .card { background: #fff; border: 1px solid #e8ebf2; border-radius: 12px; padding: 12px 16px; margin-bottom: 10px; box-shadow: 0 1px 2px rgba(22,34,55,.04); }
        .card-header { display: flex; justify-content: space-between; align-items: start; margin-bottom: 8px; }
        .card-title { font-weight: 600; color: #1b2437; font-size: 13px; }
        .card-meta { font-size: 11px; color: #98a2b4; }
        .card-body { color: #5d6a7f; font-size: 12px; line-height: 1.5; }
        .badge { display: inline-block; padding: 2px 8px; border-radius: 5px; font-size: 11px; font-weight: 600; text-transform: uppercase; }
        .badge-claude { background: #ff6b35; color: #fff; }
        .badge-gemini { background: #4285f4; color: #fff; }
        .badge-codex { background: #00d4aa; color: #04322a; }
        .badge-unknown { background: #e8ebf2; color: #5d6a7f; }
        .badge-distilled { background: #e7f8ef; color: #0e8a5a; }
        .badge-raw { background: #fdf3e3; color: #b8741a; }
        .principle-type { display: inline-block; padding: 2px 8px; border-radius: 5px; font-size: 11px; background: #eef0f6; color: #5d6a7f; }
        .impact-bar { height: 5px; background: #eef0f6; border-radius: 3px; margin-top: 10px; }
        .impact-fill { height: 100%; background: #6366f1; border-radius: 3px; }
        .loading { text-align: center; padding: 40px; color: #98a2b4; }
        .empty { text-align: center; padding: 40px; color: #98a2b4; }
        .refresh { display: inline-flex; align-items: center; gap: 7px; background: #6366f1; color: #fff; border: none; padding: 8px 16px; border-radius: 999px; cursor: pointer; font-size: 13px; }
        .refresh:hover { background: #4f46e5; }
        .concepts { display: flex; gap: 5px; flex-wrap: wrap; margin-top: 10px; }
        .concept { background: #eef0f6; padding: 2px 8px; border-radius: 5px; font-size: 11px; color: #5d6a7f; }
        .conflict-pair { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-bottom: 16px; }
        .conflict-card { background: #fff; border-radius: 12px; padding: 16px; border: 2px solid #f59e0b; }
        .conflict-card .card-title { color: #1b2437; font-weight: 600; }
        .badge-conflicting { background: #f59e0b; color: #fff; }
        .btn-keep { background: #10b981; color: #fff; border: none; padding: 8px 16px; border-radius: 8px; cursor: pointer; margin-top: 10px; width: 100%; font-size: 13px; font-weight: 600; }
        .btn-keep:hover { background: #0ea371; }
        .stat-value.amber { color: #f59e0b; }
    </style>
</head>
<body>
    <svg width="0" height="0" style="position:absolute" aria-hidden="true">
        <symbol id="ic-brain" viewBox="0 0 24 24"><path d="M12 18V5"/><path d="M15 13a4.17 4.17 0 0 1-3-4 4.17 4.17 0 0 1-3 4"/><path d="M17.598 6.5A3 3 0 1 0 12 5a3 3 0 1 0-5.598 1.5"/><path d="M17.997 5.125a4 4 0 0 1 2.526 5.77"/><path d="M18 18a4 4 0 0 0 2-7.464"/><path d="M19.967 17.483A4 4 0 1 1 12 18a4 4 0 1 1-7.967-.517"/><path d="M6 18a4 4 0 0 1-2-7.464"/><path d="M6.003 5.125a4 4 0 0 0-2.526 5.77"/></symbol>
        <symbol id="ic-activity" viewBox="0 0 24 24"><path d="M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0l-2.35 8.36A2 2 0 0 1 4.49 12H2"/></symbol>
        <symbol id="ic-sparkles" viewBox="0 0 24 24"><path d="M11.017 2.814a1 1 0 0 1 1.966 0l1.051 5.558a2 2 0 0 0 1.594 1.594l5.558 1.051a1 1 0 0 1 0 1.966l-5.558 1.051a2 2 0 0 0-1.594 1.594l-1.051 5.558a1 1 0 0 1-1.966 0l-1.051-5.558a2 2 0 0 0-1.594-1.594l-5.558-1.051a1 1 0 0 1 0-1.966l5.558-1.051a2 2 0 0 0 1.594-1.594z"/><path d="M20 2v4"/><path d="M22 4h-4"/><circle cx="4" cy="20" r="2"/></symbol>
        <symbol id="ic-scale" viewBox="0 0 24 24"><path d="M12 3v18"/><path d="m19 8 3 8a5 5 0 0 1-6 0zV7"/><path d="M3 7h1a17 17 0 0 0 8-2 17 17 0 0 0 8 2h1"/><path d="m5 8 3 8a5 5 0 0 1-6 0zV7"/><path d="M7 21h10"/></symbol>
        <symbol id="ic-rotate-cw" viewBox="0 0 24 24"><path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/></symbol>
    </svg>
    <div class="container">
        <header>
            <div class="brand">
                <div class="logo"><svg class="ic"><use href="#ic-brain"/></svg></div>
                <h1>Forge Memory Dashboard</h1>
            </div>
            <div class="stats">
                <div class="stat"><div class="stat-value" id="stat-total">-</div><div class="stat-label">Events</div></div>
                <div class="stat"><div class="stat-value" id="stat-principles">-</div><div class="stat-label">Principles</div></div>
                <div class="stat"><div class="stat-value" id="stat-sessions">-</div><div class="stat-label">Sessions</div></div>
                <div class="stat"><div class="stat-value amber" id="stat-conflicts">-</div><div class="stat-label">Conflicts</div></div>
            </div>
        </header>
        
        <div class="tabs">
            <button class="tab active" data-panel="events"><svg class="ic"><use href="#ic-activity"/></svg>Recent Events</button>
            <button class="tab" data-panel="principles"><svg class="ic"><use href="#ic-sparkles"/></svg>Principles</button>
            <button class="tab" data-panel="conflicts"><svg class="ic"><use href="#ic-scale"/></svg>Conflicts <span id="conflict-badge" style="display:none;background:#f59e0b;color:#fff;border-radius:10px;padding:1px 7px;font-size:11px;margin-left:4px"></span></button>
            <div style="margin-left: auto;"><button class="refresh" onclick="loadAll()"><svg class="ic"><use href="#ic-rotate-cw"/></svg>Refresh</button></div>
        </div>

        <div id="panel-events" class="panel active">
            <div id="events-list"><div class="loading">Loading events...</div></div>
        </div>

        <div id="panel-principles" class="panel">
            <div id="principles-list"><div class="loading">Loading principles...</div></div>
        </div>

        <div id="panel-conflicts" class="panel">
            <div id="conflicts-list"><div class="loading">Loading conflicts...</div></div>
        </div>
    </div>
    
    <script>
        async function loadStats() {
            const res = await fetch('/api/stats');
            const data = await res.json();
            document.getElementById('stat-total').textContent = data.total;
            document.getElementById('stat-principles').textContent = data.principles;
            document.getElementById('stat-sessions').textContent = data.sessions;
        }
        
        async function loadEvents() {
            const res = await fetch('/api/events?limit=50');
            const events = await res.json();
            const container = document.getElementById('events-list');
            
            if (events.length === 0) {
                container.innerHTML = '<div class="empty">No events recorded yet. Start working with Claude Code, Gemini, or Codex to capture memories.</div>';
                return;
            }
            
            container.innerHTML = events.map(e => '<div class="card"><div class="card-header"><div><span class="badge badge-' + e.source_tool + '">' + e.source_tool + '</span><span class="badge ' + (e.distilled ? 'badge-distilled' : 'badge-raw') + '">' + (e.distilled ? 'distilled' : 'raw') + '</span></div><div class="card-meta">' + e.ts.substring(0, 16) + '</div></div><div class="card-body"><strong>' + e.event_type + '</strong> ' + (e.tool_name ? '(' + e.tool_name + ')' : '') + '<br><small style="color:#666">Session: ' + e.session_id.substring(0,8) + ' | ' + projectLabel(e.project_id) + '</small><p style="margin-top:8px;font-family:monospace;font-size:12px;color:#888">' + escapeHtml(e.payload) + '</p></div></div>').join('');
        }
        
        async function loadPrinciples() {
            const res = await fetch('/api/principles?limit=1000');
            const principles = await res.json();
            const container = document.getElementById('principles-list');
            
            if (principles.length === 0) {
                container.innerHTML = '<div class="empty">No principles distilled yet. Keep working — Forge will capture patterns.</div>';
                return;
            }
            
            container.innerHTML = principles.map(p => '<div class="card"><div class="card-header"><div class="card-title">' + escapeHtml(p.title) + '</div><div><span class="principle-type">' + p.type + '</span><span class="badge badge-distilled">' + (p.impact_score * 100).toFixed(0) + '%</span></div></div><div class="card-body">' + escapeHtml(p.narrative) + '<div class="impact-bar"><div class="impact-fill" style="width:' + (p.impact_score * 100) + '%"></div></div>' + (p.concepts && p.concepts.length ? '<div class="concepts">' + p.concepts.map(function(c){return '<span class="concept">' + c + '</span>';}).join('') + '</div>' : '') + '<small style="color:#666;margin-top:8px;display:block">' + p.ts.substring(0, 10) + ' | ' + projectLabel(p.project_id) + '</small></div></div>').join('');
        }
        
        async function loadConflicts() {
            const res = await fetch('/api/conflicts');
            const pairs = await res.json();
            const container = document.getElementById('conflicts-list');
            const badge = document.getElementById('conflict-badge');
            document.getElementById('stat-conflicts').textContent = pairs.length;
            if (pairs.length > 0) {
                badge.style.display = 'inline';
                badge.textContent = pairs.length;
            } else {
                badge.style.display = 'none';
            }
            if (!pairs || pairs.length === 0) {
                container.innerHTML = '<div class="empty">No conflicting principles detected.</div>';
                return;
            }
            container.innerHTML = pairs.map(function(pair) {
                return '<div class="conflict-pair">' + renderConflictCard(pair.a, pair.b.id) + renderConflictCard(pair.b, pair.a.id) + '</div>';
            }).join('');
        }

        function renderConflictCard(p, deleteID) {
            var concepts = p.concepts && p.concepts.length ? '<div class="concepts">' + p.concepts.map(function(c){return '<span class="concept">'+c+'</span>';}).join('') + '</div>' : '';
            return '<div class="conflict-card">' +
                '<div class="card-header"><div class="card-title">' + escapeHtml(p.title) + '</div>' +
                '<div><span class="principle-type">' + p.type + '</span> <span class="badge badge-conflicting">conflicting</span></div></div>' +
                '<div class="card-body">' + escapeHtml(p.narrative) +
                '<div class="impact-bar"><div class="impact-fill" style="width:' + (p.impact_score * 100) + '%"></div></div>' +
                concepts +
                '<small style="color:#666;display:block;margin-top:6px">' + p.ts.substring(0,10) + ' | ' + projectLabel(p.project_id) + '</small>' +
                '<button class="btn-keep" onclick="resolveConflict(\'' + p.id + '\',\'' + deleteID + '\')">Keep This / Delete Other</button>' +
                '</div></div>';
        }

        async function resolveConflict(keepID, deleteID) {
            if (!confirm('Keep this principle and permanently delete the other?')) return;
            const res = await fetch('/api/conflicts/resolve', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({keep_id: keepID, delete_id: deleteID})
            });
            if (!res.ok) { alert('Resolution failed: ' + await res.text()); return; }
            await loadConflicts();
            await loadStats();
        }

        function escapeHtml(text) {
            const div = document.createElement('div');
            div.textContent = text;
            return div.innerHTML;
        }

        // Show full project_id (git root basename). Wrap in a span with title
        // tooltip so ambiguous short names like "developer" show on hover.
        function projectLabel(id) {
            if (!id) return '<span style="color:#555">unknown</span>';
            return '<span title="git root: ' + escapeHtml(id) + '" style="color:#888;font-family:monospace">' + escapeHtml(id) + '</span>';
        }

        function loadAll() {
            loadStats();
            loadEvents();
            loadPrinciples();
            loadConflicts();
        }
        
        // Tab switching
        document.querySelectorAll('.tab').forEach(tab => {
            tab.addEventListener('click', () => {
                document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
                document.querySelectorAll('.panel').forEach(p => p.classList.remove('active'));
                tab.classList.add('active');
                document.getElementById('panel-' + tab.dataset.panel).classList.add('active');
            });
        });
        
        loadAll();
    </script>
</body>
</html>
`

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
