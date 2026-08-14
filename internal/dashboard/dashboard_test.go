package dashboard

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forge/forge/internal/db"
)

func TestHandleEvents_ShortEventID(t *testing.T) {
	tmp := t.TempDir()
	database, err := db.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	defer database.Close()

	if err := database.InsertEvent(&db.Event{
		ID:         "e",
		SessionID:  "s",
		ProjectID:  "p",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    "{}",
	}); err != nil {
		t.Fatalf("InsertEvent failed: %v", err)
	}

	s := New(database, 0)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/events", nil)
	s.handleEvents(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if len(out) != 1 || out[0]["id"] != "e" {
		t.Fatalf("unexpected event response: %+v", out)
	}
}

func TestHandleTraces_ReturnsEventsTaskAndEvaluations(t *testing.T) {
	database := openDashboardTestDB(t)
	defer database.Close()

	if err := database.UpsertEvaluationTask(&db.EvaluationTask{ID: "task-1", Name: "task", Prompt: "fix it"}); err != nil {
		t.Fatalf("UpsertEvaluationTask: %v", err)
	}
	if err := database.InsertEvent(&db.Event{ID: "event-1", TraceID: "trace-1", TaskID: "task-1", SessionID: "s", ProjectID: "p", SourceTool: "codex", EventType: "PostToolUse", Payload: "{}"}); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	if err := database.InsertTraceEvaluation(&db.TraceEvaluation{TraceID: "trace-1", TaskID: "task-1", Evaluator: "grader", TaskSuccess: true, Score: 1}); err != nil {
		t.Fatalf("InsertTraceEvaluation: %v", err)
	}

	s := New(database, 0)
	rr := httptest.NewRecorder()
	s.handleTraces(rr, httptest.NewRequest(http.MethodGet, "/api/traces?trace_id=trace-1", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out struct {
		TraceID     string               `json:"trace_id"`
		Task        *db.EvaluationTask   `json:"task"`
		Events      []db.Event           `json:"events"`
		Evaluations []db.TraceEvaluation `json:"evaluations"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if out.TraceID != "trace-1" || out.Task == nil || len(out.Events) != 1 || len(out.Evaluations) != 1 || !out.Evaluations[0].TaskSuccess {
		t.Fatalf("unexpected trace response: %+v", out)
	}
}

func TestHandleEvaluationTaskAndEvaluation_PostsResults(t *testing.T) {
	database := openDashboardTestDB(t)
	defer database.Close()
	s := New(database, 0)

	taskBody := `{"id":"task-api","prompt":"fix the bug","rubric_version":"r2"}`
	taskResponse := httptest.NewRecorder()
	s.handleEvaluationTask(taskResponse, httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(taskBody)))
	if taskResponse.Code != http.StatusCreated {
		t.Fatalf("task status = %d, want 201", taskResponse.Code)
	}

	evalBody := `{"trace_id":"trace-api","task_id":"task-api","evaluator":"grader-v2","task_success":true,"score":0.9}`
	evalResponse := httptest.NewRecorder()
	s.handleEvaluation(evalResponse, httptest.NewRequest(http.MethodPost, "/api/evaluations", strings.NewReader(evalBody)))
	if evalResponse.Code != http.StatusCreated {
		t.Fatalf("evaluation status = %d, want 201", evalResponse.Code)
	}
	evaluations, err := database.TraceEvaluations("trace-api")
	if err != nil || len(evaluations) != 1 || !evaluations[0].TaskSuccess {
		t.Fatalf("stored evaluations = %#v, err=%v", evaluations, err)
	}
	reportResponse := httptest.NewRecorder()
	s.handleEvaluationReport(reportResponse, httptest.NewRequest(http.MethodGet, "/api/evaluations/report?task_id=task-api", nil))
	if reportResponse.Code != http.StatusOK || !strings.Contains(reportResponse.Body.String(), `"runs":1`) {
		t.Fatalf("report response status=%d body=%s", reportResponse.Code, reportResponse.Body.String())
	}
}

func TestHandleArtifacts_UploadListAndDownload(t *testing.T) {
	database := openDashboardTestDB(t)
	defer database.Close()
	s := New(database, 0)
	body := `{"trace_id":"trace-artifact","task_id":"task-1","kind":"diff","media_type":"text/x-diff","content_base64":"` + base64.StdEncoding.EncodeToString([]byte("+fixed\n")) + `"}`
	upload := httptest.NewRecorder()
	s.handleArtifacts(upload, httptest.NewRequest(http.MethodPost, "/api/artifacts", strings.NewReader(body)))
	if upload.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, body=%s", upload.Code, upload.Body.String())
	}
	var artifact db.EvaluationArtifact
	if err := json.Unmarshal(upload.Body.Bytes(), &artifact); err != nil {
		t.Fatalf("upload JSON: %v", err)
	}
	list := httptest.NewRecorder()
	s.handleArtifacts(list, httptest.NewRequest(http.MethodGet, "/api/artifacts?trace_id=trace-artifact", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), artifact.ID) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	download := httptest.NewRecorder()
	s.handleArtifact(download, httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifact.ID, nil))
	if download.Code != http.StatusOK || download.Body.String() != "+fixed\n" {
		t.Fatalf("download status=%d body=%q", download.Code, download.Body.String())
	}
}

func TestHandlePrinciples_ShortPrincipleID(t *testing.T) {
	tmp := t.TempDir()
	database, err := db.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	defer database.Close()

	if _, err := database.InsertPrinciple(&db.Principle{
		ID:        "p",
		TS:        "2026-01-01T00:00:00Z",
		Type:      "pattern",
		Title:     "title",
		Narrative: "narrative",
		ProjectID: "proj",
	}); err != nil {
		t.Fatalf("InsertPrinciple failed: %v", err)
	}

	s := New(database, 0)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/principles", nil)
	s.handlePrinciples(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if len(out) != 1 || out[0]["id"] != "p" {
		t.Fatalf("unexpected principle response: %+v", out)
	}
}

func openDashboardTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func insertConflictingPair(t *testing.T, d *db.DB, titleA, titleB, project string) (db.Principle, db.Principle) {
	t.Helper()
	a := &db.Principle{Type: "pattern", Title: titleA, Narrative: titleA, ImpactScore: 0.8, ProjectID: project}
	b := &db.Principle{Type: "pattern", Title: titleB, Narrative: titleB, ImpactScore: 0.7, ProjectID: project}
	if _, err := d.InsertPrinciple(a); err != nil {
		t.Fatalf("InsertPrinciple a: %v", err)
	}
	if _, err := d.InsertPrinciple(b); err != nil {
		t.Fatalf("InsertPrinciple b: %v", err)
	}
	if err := d.MarkConflicting(a.ID, b.ID); err != nil {
		t.Fatalf("MarkConflicting: %v", err)
	}
	return *a, *b
}

func TestHandleConflicts_Empty(t *testing.T) {
	d := openDashboardTestDB(t)
	s := New(d, 0)
	rr := httptest.NewRecorder()
	s.handleConflicts(rr, httptest.NewRequest(http.MethodGet, "/api/conflicts", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out []conflictPairJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty pairs, got %d", len(out))
	}
}

func TestHandleConflicts_ReturnsPairs(t *testing.T) {
	d := openDashboardTestDB(t)
	a, b := insertConflictingPair(t, d, "Redis session cache use", "Avoid Redis session cache", "proj")

	s := New(d, 0)
	rr := httptest.NewRecorder()
	s.handleConflicts(rr, httptest.NewRequest(http.MethodGet, "/api/conflicts", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	var out []conflictPairJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// Should be exactly 1 pair (deduplicated from 2 conflicting rows).
	if len(out) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(out))
	}
	ids := map[string]bool{out[0].A.ID: true, out[0].B.ID: true}
	if !ids[a.ID] || !ids[b.ID] {
		t.Errorf("pair IDs %v do not contain expected %q and %q", ids, a.ID, b.ID)
	}
}

func TestHandleResolveConflict_KeepsWinner(t *testing.T) {
	d := openDashboardTestDB(t)
	keep, del := insertConflictingPair(t, d, "Redis session cache good", "Redis session cache bad", "proj")

	s := New(d, 0)
	body := strings.NewReader(`{"keep_id":"` + keep.ID + `","delete_id":"` + del.ID + `"}`)
	rr := httptest.NewRecorder()
	s.handleResolveConflict(rr, httptest.NewRequest(http.MethodPost, "/api/conflicts/resolve", body))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	winner, err := d.GetPrincipleByID(keep.ID)
	if err != nil || winner == nil {
		t.Fatalf("winner not found: %v", err)
	}
	if winner.Status != "active" {
		t.Errorf("winner.Status = %q, want active", winner.Status)
	}
	loser, err := d.GetPrincipleByID(del.ID)
	if err != nil {
		t.Fatalf("GetPrincipleByID(loser): %v", err)
	}
	if loser != nil {
		t.Error("loser should have been deleted")
	}
}

func TestHandleResolveConflict_InvalidInputs(t *testing.T) {
	d := openDashboardTestDB(t)
	s := New(d, 0)

	// GET should return 405.
	rr := httptest.NewRecorder()
	s.handleResolveConflict(rr, httptest.NewRequest(http.MethodGet, "/api/conflicts/resolve", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rr.Code)
	}

	// POST with empty body should return 400.
	rr = httptest.NewRecorder()
	s.handleResolveConflict(rr, httptest.NewRequest(http.MethodPost, "/api/conflicts/resolve", strings.NewReader("{}")))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("empty body status = %d, want 400", rr.Code)
	}
}

func TestShort(t *testing.T) {
	cases := []struct {
		input string
		n     int
		want  string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello"},
		{"abc", 3, "abc"},
		{"", 5, ""},
	}
	for _, tc := range cases {
		got := short(tc.input, tc.n)
		if got != tc.want {
			t.Errorf("short(%q, %d) = %q, want %q", tc.input, tc.n, got, tc.want)
		}
	}
}

func TestHandleStats_ReturnsJSON(t *testing.T) {
	d := openDashboardTestDB(t)

	if err := d.InsertEvent(&db.Event{
		SessionID:  "s",
		ProjectID:  "p",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    "{}",
	}); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	s := New(d, 0)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	s.handleStats(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := out["total"]; !ok {
		t.Error("expected 'total' in stats response")
	}
	if _, ok := out["principles"]; !ok {
		t.Error("expected 'principles' in stats response")
	}
}
