package funstats

import (
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
)

func evAt(hour int, eventType, payload string) db.Event {
	t := time.Date(2026, 1, 1, hour, 0, 0, 0, time.UTC)
	return db.Event{TS: t.Format(time.RFC3339), EventType: eventType, Payload: payload}
}

func TestPeakHour(t *testing.T) {
	events := []db.Event{evAt(2, "PostToolUse", ""), evAt(2, "PostToolUse", ""), evAt(14, "PostToolUse", "")}
	if got := PeakHour(events); got != 2 {
		t.Errorf("peak hour = %d, want 2", got)
	}
}

func TestTopKeywords(t *testing.T) {
	events := []db.Event{
		evAt(1, "UserPromptSubmit", `"fix the auth bug"`),
		evAt(2, "UserPromptSubmit", `"fix the auth bug again"`),
		evAt(3, "PostToolUse", `"fix"`), // wrong event type, must be ignored
	}
	extract := func(p string) string { return p }
	top := TopKeywords(events, 2, extract)
	if len(top) != 2 || top[0] != "auth" && top[0] != "bug" && top[0] != "fix" {
		t.Errorf("unexpected top keywords: %v", top)
	}
}

func TestMaxConcurrentSessions(t *testing.T) {
	ranges := []db.SessionRange{
		{SessionID: "a", Lo: "2026-01-01T09:00:00Z", Hi: "2026-01-01T10:00:00Z"},
		{SessionID: "b", Lo: "2026-01-01T09:30:00Z", Hi: "2026-01-01T09:45:00Z"},
		{SessionID: "c", Lo: "2026-01-02T09:00:00Z", Hi: "2026-01-02T09:10:00Z"},
	}
	if got := MaxConcurrentSessions(ranges); got != 2 {
		t.Errorf("max concurrent = %d, want 2", got)
	}
}

func TestMaxConcurrentSessions_NoOverlap(t *testing.T) {
	ranges := []db.SessionRange{
		{SessionID: "a", Lo: "2026-01-01T09:00:00Z", Hi: "2026-01-01T10:00:00Z"},
		{SessionID: "b", Lo: "2026-01-01T11:00:00Z", Hi: "2026-01-01T12:00:00Z"},
	}
	if got := MaxConcurrentSessions(ranges); got != 1 {
		t.Errorf("max concurrent = %d, want 1", got)
	}
}

func TestArchetype(t *testing.T) {
	if got := Archetype(2, 0.1, 1); got != "Night Owl" {
		t.Errorf("got %q, want Night Owl", got)
	}
	if got := Archetype(14, 0.1, 3); got != "Multi-Agent Conductor" {
		t.Errorf("got %q, want Multi-Agent Conductor", got)
	}
	if got := Archetype(14, 0.02, 1); got != "Autopilot Truster" {
		t.Errorf("got %q, want Autopilot Truster", got)
	}
}
