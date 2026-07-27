package steering

import (
	"testing"

	"github.com/forge/forge/internal/db"
)

func ev(t string) db.Event { return db.Event{EventType: t} }

func TestCompute(t *testing.T) {
	events := []db.Event{
		ev("UserPromptSubmit"), // 1st prompt, never steering
		ev("PostToolUse"),
		ev("Stop"),
		ev("UserPromptSubmit"), // after Stop, not steering
		ev("PostToolUse"),
		ev("UserPromptSubmit"), // no Stop since prior prompt -> steering
		ev("Stop"),
	}
	s := Compute(events)
	if s.TotalPrompts != 3 {
		t.Fatalf("total = %d, want 3", s.TotalPrompts)
	}
	if s.SteeredPrompts != 1 {
		t.Fatalf("steered = %d, want 1", s.SteeredPrompts)
	}
	if got, want := s.Rate(), 1.0/3.0; got != want {
		t.Errorf("rate = %v, want %v", got, want)
	}
}

func TestCompute_Empty(t *testing.T) {
	s := Compute(nil)
	if s.Rate() != 0 {
		t.Errorf("empty rate should be 0, got %v", s.Rate())
	}
}

func TestCompute_LowercaseVariant(t *testing.T) {
	events := []db.Event{
		ev("UserPromptSubmit"),
		ev("stop"),
		ev("UserPromptSubmit"),
	}
	s := Compute(events)
	if s.SteeredPrompts != 0 {
		t.Errorf("lowercase stop should still count, got %d steered", s.SteeredPrompts)
	}
}
