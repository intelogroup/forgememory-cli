package mcp

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/forge/forge/internal/db"
)

func (s *Server) getActiveFailures(args map[string]any) ToolResult {
	limit := intFromArgs(args, "limit", 5)
	projectID := projectIDFromArgs(args)

	alerts, err := s.db.ActiveAlertsByProject(projectID, limit)
	if err != nil {
		return toolError("Failed to get active failures: " + err.Error())
	}

	text := fmt.Sprintf("## Active Failures: %s\n\n", projectID)
	if len(alerts) == 0 {
		text += "No active failures.\n"
	} else {
		for _, alert := range alerts {
			text += fmt.Sprintf("### %s [%s]\n", alert.Title, alert.Severity)
			text += fmt.Sprintf("- %s\n", alert.Narrative)
			if alert.SourceRef != "" {
				text += fmt.Sprintf("- **Source Ref**: %s\n", alert.SourceRef)
			}
			text += fmt.Sprintf("- **Score**: %.1f\n\n", alert.Score)
		}
	}

	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: text}},
	}
}

type coachingStatusPayload struct {
	ProjectID string         `json:"project_id"`
	Total     int            `json:"total"`
	Counts    map[string]int `json:"counts"`
}

type coachingItemPayload struct {
	ID            string  `json:"id"`
	ObservationID string  `json:"observation_id"`
	SkillKey      string  `json:"skill_key"`
	ProjectID     string  `json:"project_id"`
	Status        string  `json:"status"`
	DeliveryMode  string  `json:"delivery_mode"`
	Question      string  `json:"question"`
	NextAction    string  `json:"next_action"`
	Lesson        string  `json:"lesson"`
	CreatedAt     string  `json:"created_at"`
	SurfacedAt    string  `json:"surfaced_at"`
	ResolvedAt    string  `json:"resolved_at"`
	Resolution    string  `json:"resolution"`
	Confidence    float64 `json:"confidence"`
	State         string  `json:"state"`
	EvidenceCount int     `json:"evidence_count"`
}

type coachingItemsPayload struct {
	ProjectID string                `json:"project_id"`
	Items     []coachingItemPayload `json:"items"`
}

type coachingExplanationPayload struct {
	Item               coachingItemPayload      `json:"item"`
	Observation        db.Observation           `json:"observation"`
	SupportingEvidence []db.ObservationEvidence `json:"supporting_evidence"`
	CounterEvidence    []db.ObservationEvidence `json:"counter_evidence"`
}

func (s *Server) getCoachingStatus(args map[string]any) ToolResult {
	projectID := projectIDFromArgs(args)
	items, err := s.db.ListAllCoachingItems(projectID, "")
	if err != nil {
		return toolError(fmt.Sprintf("list coaching items: %v", err))
	}
	counts := map[string]int{}
	for _, item := range items {
		counts[item.Status]++
	}
	for _, status := range []string{"queued", "surfaced", "accepted", "deferred", "dismissed"} {
		if _, ok := counts[status]; !ok {
			counts[status] = 0
		}
	}
	payload := coachingStatusPayload{ProjectID: projectID, Total: len(items), Counts: counts}
	return jsonToolResult(payload)
}

func (s *Server) listCoachingItems(args map[string]any) ToolResult {
	projectID := projectIDFromArgs(args)
	status := strings.TrimSpace(stringFromArgs(args, "status"))
	limit := intFromArgs(args, "limit", 20)
	items, err := s.db.ListCoachingItems(projectID, status, limit)
	if err != nil {
		return toolError(fmt.Sprintf("list coaching items: %v", err))
	}
	payload := coachingItemsPayload{ProjectID: projectID, Items: make([]coachingItemPayload, 0, len(items))}
	for _, item := range items {
		entry, err := s.coachingItemPayload(item)
		if err != nil {
			return toolError(err.Error())
		}
		payload.Items = append(payload.Items, entry)
	}
	return jsonToolResult(payload)
}

func (s *Server) explainCoachingItem(args map[string]any) ToolResult {
	id := strings.TrimSpace(stringFromArgs(args, "item_id"))
	if id == "" {
		return toolError("item_id is required")
	}
	item, err := s.coachingItemByID(id)
	if err != nil {
		return toolError(err.Error())
	}
	if requestedProject := projectIDFromOptionalArgs(args); requestedProject != "" && requestedProject != item.ProjectID {
		return toolError(fmt.Sprintf("coaching item %q was not found", id))
	}
	observation, found, err := s.db.ObservationByID(item.ObservationID)
	if err != nil {
		return toolError(fmt.Sprintf("get observation: %v", err))
	}
	if !found {
		return toolError(fmt.Sprintf("observation %q was not found", item.ObservationID))
	}
	evidence, err := s.db.ListObservationEvidence(item.ObservationID)
	if err != nil {
		return toolError(fmt.Sprintf("list observation evidence: %v", err))
	}
	entry, err := s.coachingItemPayload(item)
	if err != nil {
		return toolError(err.Error())
	}
	payload := coachingExplanationPayload{
		Item:               entry,
		Observation:        observation,
		SupportingEvidence: make([]db.ObservationEvidence, 0),
		CounterEvidence:    make([]db.ObservationEvidence, 0),
	}
	for _, source := range evidence {
		switch source.Role {
		case "supporting":
			payload.SupportingEvidence = append(payload.SupportingEvidence, source)
		case "counter":
			payload.CounterEvidence = append(payload.CounterEvidence, source)
		}
	}
	return jsonToolResult(payload)
}

func (s *Server) coachingItemPayload(item db.CoachingItem) (coachingItemPayload, error) {
	entry := coachingItemPayload{
		ID: item.ID, ObservationID: item.ObservationID, SkillKey: item.SkillKey, ProjectID: item.ProjectID,
		Status: item.Status, DeliveryMode: item.DeliveryMode, Question: item.Question, NextAction: item.NextAction,
		Lesson: item.Lesson, CreatedAt: item.CreatedAt, SurfacedAt: item.SurfacedAt, ResolvedAt: item.ResolvedAt,
		Resolution: item.Resolution,
	}
	state, err := s.db.GetSkillState(item.SkillKey, "project", item.ProjectID)
	if err != nil {
		return coachingItemPayload{}, fmt.Errorf("get skill state: %v", err)
	}
	if state != nil {
		entry.Confidence = state.Confidence
		entry.State = state.State
		entry.EvidenceCount = state.EvidenceCount
	}
	return entry, nil
}

func (s *Server) coachingItemByID(id string) (db.CoachingItem, error) {
	var item db.CoachingItem
	err := s.db.Conn().QueryRow(`SELECT id, observation_id, skill_key, project_id, status, delivery_mode, question, next_action, lesson,
		created_at, surfaced_at, resolved_at, resolution FROM coaching_items WHERE id=?`, id).Scan(
		&item.ID, &item.ObservationID, &item.SkillKey, &item.ProjectID, &item.Status, &item.DeliveryMode, &item.Question,
		&item.NextAction, &item.Lesson, &item.CreatedAt, &item.SurfacedAt, &item.ResolvedAt, &item.Resolution)
	if err == sql.ErrNoRows {
		return db.CoachingItem{}, fmt.Errorf("coaching item %q was not found", id)
	}
	if err != nil {
		return db.CoachingItem{}, fmt.Errorf("get coaching item: %v", err)
	}
	return item, nil
}

func projectIDFromOptionalArgs(args map[string]any) string {
	if strings.TrimSpace(stringFromArgs(args, "project_id")) == "" {
		return ""
	}
	return projectIDFromArgs(args)
}

func jsonToolResult(value any) ToolResult {
	data, err := json.Marshal(value)
	if err != nil {
		return toolError(fmt.Sprintf("encode coaching result: %v", err))
	}
	return ToolResult{Content: []ToolContent{{Type: "text", Text: string(data)}}, IsError: false}
}
