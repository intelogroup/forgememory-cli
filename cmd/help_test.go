package main

import (
	"strings"
	"testing"
)

func TestAgentGuideSurfacesProviderSetupCommands(t *testing.T) {
	out := captureStdout(func() { runAgentGuide(nil) })

	for _, want := range []string{
		"## Provider Setup",
		"~/.forge/config",
		"forge config --provider ollama --model llama3:latest",
		"forge config --provider openai --api-key sk-...",
		"forge config --provider anthropic --api-key sk-ant-...",
		"forge config --provider groq --api-key gsk-...",
		"forge config --provider ollama --model llama3:latest",
		"ollama pull llama3",
		"forge distill",
		"forge health",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("agent guide missing %q:\n%s", want, out)
		}
	}
}

func TestTopLevelHelpSurfacesProviderSetupCommands(t *testing.T) {
	out := captureStdout(printUsage)

	for _, want := range []string{
		"Provider setup:",
		"forge config --show",
		"forge config --provider ollama --model llama3:latest",
		"forge config --provider openai --api-key sk-...",
		"forge config --provider anthropic --api-key sk-ant-...",
		"forge config --provider groq --api-key gsk-...",
		"forge config --provider ollama --model llama3:latest",
		"FORGE_PROVIDER      Inference provider: anthropic/openai/groq/nvidia/ollama/codex",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("top-level help missing %q:\n%s", want, out)
		}
	}
}
