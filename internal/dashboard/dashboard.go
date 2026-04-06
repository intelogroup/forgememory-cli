package dashboard

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/forge/forge/internal/db"
)

type Server struct {
	db     *db.DB
	port   int
	server *http.Server
}

func New(database *db.DB, port int) *Server {
	return &Server{
		db:   database,
		port: port,
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/events", s.handleEvents)
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
		ID         string `json:"id"`
		TS         string `json:"ts"`
		SessionID  string `json:"session_id"`
		ProjectID  string `json:"project_id"`
		SourceTool string `json:"source_tool"`
		EventType  string `json:"event_type"`
		ToolName   string `json:"tool_name"`
		Payload    string `json:"payload"`
		Distilled  bool   `json:"distilled"`
	}

	var result []EventJSON
	for _, e := range events {
		payload := e.Payload
		if len(payload) > 200 {
			payload = payload[:200] + "..."
		}
		result = append(result, EventJSON{
			ID:         short(e.ID, 8),
			TS:         e.TS,
			SessionID:  e.SessionID,
			ProjectID:  e.ProjectID,
			SourceTool: e.SourceTool,
			EventType:  e.EventType,
			ToolName:   e.ToolName,
			Payload:    payload,
			Distilled:  e.Distilled,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *Server) handlePrinciples(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 50 {
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
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #0a0a0f; color: #e0e0e0; min-height: 100vh; }
        .container { max-width: 1200px; margin: 0 auto; padding: 20px; }
        header { display: flex; align-items: center; justify-content: space-between; padding: 20px 0; border-bottom: 1px solid #222; }
        h1 { font-size: 24px; font-weight: 600; color: #fff; }
        .stats { display: flex; gap: 20px; }
        .stat { background: #16161f; padding: 12px 20px; border-radius: 8px; text-align: center; }
        .stat-value { font-size: 28px; font-weight: 700; color: #6366f1; }
        .stat-label { font-size: 12px; color: #888; margin-top: 4px; }
        .tabs { display: flex; gap: 4px; margin: 20px 0; }
        .tab { padding: 10px 20px; background: transparent; border: none; color: #888; cursor: pointer; border-radius: 6px; font-size: 14px; }
        .tab:hover { background: #16161f; color: #fff; }
        .tab.active { background: #6366f1; color: #fff; }
        .panel { display: none; }
        .panel.active { display: block; }
        .card { background: #16161f; border-radius: 12px; padding: 16px; margin-bottom: 12px; border: 1px solid #222; }
        .card-header { display: flex; justify-content: space-between; align-items: start; margin-bottom: 8px; }
        .card-title { font-weight: 600; color: #fff; }
        .card-meta { font-size: 12px; color: #666; }
        .card-body { color: #aaa; font-size: 14px; line-height: 1.5; }
        .badge { display: inline-block; padding: 2px 8px; border-radius: 4px; font-size: 11px; font-weight: 600; text-transform: uppercase; }
        .badge-claude { background: #ff6b35; color: #fff; }
        .badge-gemini { background: #4285f4; color: #fff; }
        .badge-codex { background: #00d4aa; color: #000; }
        .badge-unknown { background: #666; color: #fff; }
        .badge-distilled { background: #22c55e; color: #fff; }
        .badge-raw { background: #f59e0b; color: #000; }
        .principle-type { display: inline-block; padding: 2px 8px; border-radius: 4px; font-size: 11px; background: #2a2a3a; color: #a0a0a0; }
        .impact-bar { height: 4px; background: #333; border-radius: 2px; margin-top: 8px; }
        .impact-fill { height: 100%; background: #6366f1; border-radius: 2px; }
        .loading { text-align: center; padding: 40px; color: #666; }
        .empty { text-align: center; padding: 40px; color: #666; }
        .refresh { background: #6366f1; color: #fff; border: none; padding: 8px 16px; border-radius: 6px; cursor: pointer; font-size: 13px; }
        .refresh:hover { background: #4f46e5; }
        .concepts { display: flex; gap: 4px; flex-wrap: wrap; margin-top: 8px; }
        .concept { background: #222; padding: 2px 6px; border-radius: 3px; font-size: 11px; color: #888; }
        .conflict-pair { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-bottom: 16px; }
        .conflict-card { background: #16161f; border-radius: 12px; padding: 16px; border: 2px solid #f59e0b; }
        .conflict-card .card-title { color: #fff; font-weight: 600; }
        .badge-conflicting { background: #f59e0b; color: #000; }
        .btn-keep { background: #22c55e; color: #fff; border: none; padding: 8px 16px; border-radius: 6px; cursor: pointer; margin-top: 10px; width: 100%; font-size: 13px; }
        .btn-keep:hover { background: #16a34a; }
        .stat-value.amber { color: #f59e0b; }
    </style>
</head>
<body>
    <div class="container">
        <header>
            <h1>Forge Memory Dashboard</h1>
            <div class="stats">
                <div class="stat"><div class="stat-value" id="stat-total">-</div><div class="stat-label">Events</div></div>
                <div class="stat"><div class="stat-value" id="stat-principles">-</div><div class="stat-label">Principles</div></div>
                <div class="stat"><div class="stat-value" id="stat-sessions">-</div><div class="stat-label">Sessions</div></div>
                <div class="stat"><div class="stat-value amber" id="stat-conflicts">-</div><div class="stat-label">Conflicts</div></div>
            </div>
        </header>
        
        <div class="tabs">
            <button class="tab active" data-panel="events">Recent Events</button>
            <button class="tab" data-panel="principles">Principles</button>
            <button class="tab" data-panel="conflicts">Conflicts <span id="conflict-badge" style="display:none;background:#f59e0b;color:#000;border-radius:10px;padding:1px 7px;font-size:11px;margin-left:4px"></span></button>
            <div style="margin-left: auto;"><button class="refresh" onclick="loadAll()">Refresh</button></div>
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
            
            container.innerHTML = events.map(e => '<div class="card"><div class="card-header"><div><span class="badge badge-' + e.source_tool + '">' + e.source_tool + '</span><span class="badge ' + (e.distilled ? 'badge-distilled' : 'badge-raw') + '">' + (e.distilled ? 'distilled' : 'raw') + '</span></div><div class="card-meta">' + e.ts.substring(0, 16) + '</div></div><div class="card-body"><strong>' + e.event_type + '</strong> ' + (e.tool_name ? '(' + e.tool_name + ')' : '') + '<br><small style="color:#666">Session: ' + e.session_id.substring(0,8) + ' | Project: ' + e.project_id + '</small><p style="margin-top:8px;font-family:monospace;font-size:12px;color:#888">' + escapeHtml(e.payload) + '</p></div></div>').join('');
        }
        
        async function loadPrinciples() {
            const res = await fetch('/api/principles?limit=20');
            const principles = await res.json();
            const container = document.getElementById('principles-list');
            
            if (principles.length === 0) {
                container.innerHTML = '<div class="empty">No principles distilled yet. Keep working — Forge will capture patterns.</div>';
                return;
            }
            
            container.innerHTML = principles.map(p => '<div class="card"><div class="card-header"><div class="card-title">' + escapeHtml(p.title) + '</div><div><span class="principle-type">' + p.type + '</span><span class="badge badge-distilled">' + (p.impact_score * 100).toFixed(0) + '%</span></div></div><div class="card-body">' + escapeHtml(p.narrative) + '<div class="impact-bar"><div class="impact-fill" style="width:' + (p.impact_score * 100) + '%"></div></div>' + (p.concepts && p.concepts.length ? '<div class="concepts">' + p.concepts.map(function(c){return '<span class="concept">' + c + '</span>';}).join('') + '</div>' : '') + '<small style="color:#666;margin-top:8px;display:block">' + p.ts.substring(0, 10) + ' | Project: ' + p.project_id + '</small></div></div>').join('');
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
                '<small style="color:#666;display:block;margin-top:6px">' + p.ts.substring(0,10) + ' | ' + p.project_id + '</small>' +
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
