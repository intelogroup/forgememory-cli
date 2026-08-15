package db

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// EvaluationTask describes a repeatable task that can have multiple agent
// traces compared against the same prompt, baseline, tests, and rubric.
type EvaluationTask struct {
	ID             string `json:"id"`
	CreatedAt      string `json:"created_at"`
	ProjectID      string `json:"project_id"`
	Name           string `json:"name"`
	Prompt         string `json:"prompt"`
	BaselineCommit string `json:"baseline_commit"`
	ExpectedTests  string `json:"expected_tests"`
	RubricVersion  string `json:"rubric_version"`
	Metadata       string `json:"metadata"`
}

// TraceEvaluation stores one evaluator's result without changing raw trace
// events. Re-evaluating with a new model or rubric creates a new versioned row.
type TraceEvaluation struct {
	ID              string  `json:"id"`
	CreatedAt       string  `json:"created_at"`
	TraceID         string  `json:"trace_id"`
	TaskID          string  `json:"task_id"`
	Evaluator       string  `json:"evaluator"`
	RubricVersion   string  `json:"rubric_version"`
	TaskSuccess     bool    `json:"task_success"`
	Score           float64 `json:"score"`
	FailureCategory string  `json:"failure_category"`
	Rationale       string  `json:"rationale"`
	EvidenceSpans   string  `json:"evidence_spans"`
}

type EvaluationAggregate struct {
	TaskID        string         `json:"task_id"`
	Evaluator     string         `json:"evaluator"`
	RubricVersion string         `json:"rubric_version"`
	Runs          int            `json:"runs"`
	Successes     int            `json:"successes"`
	SuccessRate   float64        `json:"success_rate"`
	AverageScore  float64        `json:"average_score"`
	Failures      map[string]int `json:"failures"`
}

// UpsertEvaluationTask creates or updates the task definition by stable ID.
func (d *DB) UpsertEvaluationTask(task *EvaluationTask) error {
	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	if task.CreatedAt == "" {
		task.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if task.RubricVersion == "" {
		task.RubricVersion = "1"
	}
	if task.Metadata == "" {
		task.Metadata = "{}"
	}
	_, err := d.conn.Exec(`
		INSERT INTO evaluation_tasks (id, created_at, project_id, name, prompt, baseline_commit, expected_tests, rubric_version, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		 project_id=excluded.project_id, name=excluded.name, prompt=excluded.prompt,
		 baseline_commit=excluded.baseline_commit, expected_tests=excluded.expected_tests,
		 rubric_version=excluded.rubric_version, metadata=excluded.metadata`,
		task.ID, task.CreatedAt, task.ProjectID, task.Name, task.Prompt, task.BaselineCommit,
		task.ExpectedTests, task.RubricVersion, task.Metadata,
	)
	return err
}

// InsertTraceEvaluation stores an evaluation result. The same evaluator and
// rubric version for a trace are replaced so reruns remain deterministic.
func (d *DB) InsertTraceEvaluation(evaluation *TraceEvaluation) error {
	if evaluation.ID == "" {
		evaluation.ID = uuid.NewString()
	}
	if evaluation.CreatedAt == "" {
		evaluation.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if evaluation.RubricVersion == "" {
		evaluation.RubricVersion = "1"
	}
	if evaluation.EvidenceSpans == "" {
		evaluation.EvidenceSpans = "[]"
	}
	_, err := d.conn.Exec(`
		INSERT INTO trace_evaluations (id, created_at, trace_id, task_id, evaluator, rubric_version, task_success, score, failure_category, rationale, evidence_spans)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(trace_id, evaluator, rubric_version) DO UPDATE SET
		 created_at=excluded.created_at, task_id=excluded.task_id, task_success=excluded.task_success,
		 score=excluded.score, failure_category=excluded.failure_category,
		 rationale=excluded.rationale, evidence_spans=excluded.evidence_spans`,
		evaluation.ID, evaluation.CreatedAt, evaluation.TraceID, evaluation.TaskID, evaluation.Evaluator,
		evaluation.RubricVersion, boolInt(evaluation.TaskSuccess), evaluation.Score,
		evaluation.FailureCategory, evaluation.Rationale, evaluation.EvidenceSpans,
	)
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// TraceEvaluations returns all evaluator results for one trace.
func (d *DB) TraceEvaluations(traceID string) ([]TraceEvaluation, error) {
	rows, err := d.conn.Query(`
		SELECT id, created_at, trace_id, task_id, evaluator, rubric_version,
		       task_success, score, failure_category, rationale, evidence_spans
		FROM trace_evaluations WHERE trace_id=? ORDER BY created_at DESC`, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var evaluations []TraceEvaluation
	for rows.Next() {
		var evaluation TraceEvaluation
		var success int
		if err := rows.Scan(&evaluation.ID, &evaluation.CreatedAt, &evaluation.TraceID, &evaluation.TaskID, &evaluation.Evaluator, &evaluation.RubricVersion, &success, &evaluation.Score, &evaluation.FailureCategory, &evaluation.Rationale, &evaluation.EvidenceSpans); err != nil {
			return nil, err
		}
		evaluation.TaskSuccess = success == 1
		evaluations = append(evaluations, evaluation)
	}
	return evaluations, rows.Err()
}

// EvaluationTaskByID returns a task definition for API and evaluator clients.
func (d *DB) EvaluationTaskByID(id string) (*EvaluationTask, error) {
	var task EvaluationTask
	err := d.conn.QueryRow(`
		SELECT id, created_at, project_id, name, prompt, baseline_commit,
		       expected_tests, rubric_version, metadata
		FROM evaluation_tasks WHERE id=?`, id).Scan(
		&task.ID, &task.CreatedAt, &task.ProjectID, &task.Name, &task.Prompt,
		&task.BaselineCommit, &task.ExpectedTests, &task.RubricVersion, &task.Metadata,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

// EvaluationReport aggregates scored traces for a task. Empty taskID returns
// the complete local evaluation corpus.
func (d *DB) EvaluationReport(taskID string) ([]EvaluationAggregate, error) {
	query := `SELECT task_id, evaluator, rubric_version, task_success, score, failure_category FROM trace_evaluations`
	args := []any{}
	if taskID != "" {
		query += ` WHERE task_id=?`
		args = append(args, taskID)
	}
	query += ` ORDER BY task_id, evaluator, rubric_version, created_at`
	rows, err := d.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ taskID, evaluator, rubric string }
	indexes := map[key]int{}
	var report []EvaluationAggregate
	for rows.Next() {
		var task, evaluator, rubric, failure string
		var success int
		var score float64
		if err := rows.Scan(&task, &evaluator, &rubric, &success, &score, &failure); err != nil {
			return nil, err
		}
		k := key{task, evaluator, rubric}
		i, ok := indexes[k]
		if !ok {
			i = len(report)
			indexes[k] = i
			report = append(report, EvaluationAggregate{TaskID: task, Evaluator: evaluator, RubricVersion: rubric, Failures: map[string]int{}})
		}
		report[i].Runs++
		report[i].Successes += success
		report[i].AverageScore += score
		if failure != "" {
			report[i].Failures[failure]++
		}
	}
	for i := range report {
		if report[i].Runs > 0 {
			report[i].SuccessRate = float64(report[i].Successes) / float64(report[i].Runs)
			report[i].AverageScore /= float64(report[i].Runs)
		}
	}
	return report, rows.Err()
}
