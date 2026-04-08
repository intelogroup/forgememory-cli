package retrieve

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forge/forge/internal/db"
)

func TestWorkerRunOnce_StoresContext7MCPSummaryAndMarksJobDone(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	t.Setenv("FORGE_CONTEXT7_MCP_CMD", os.Args[0])
	t.Setenv("FORGE_CONTEXT7_MCP_ARGS", "-test.run=TestContext7MCPHelperProcess --")
	t.Setenv("GO_WANT_CONTEXT7_MCP_HELPER", "1")

	if err := database.InsertRetrievalJob(&db.RetrievalJob{
		ProjectID:   "api-service",
		TriggerType: "prompt",
		Source:      "context7",
		Query:       "How do I use React hooks correctly?",
		LibraryName: "react",
		Status:      "queued",
		Priority:    90,
		DedupeKey:   "job-mcp-1",
	}); err != nil {
		t.Fatalf("InsertRetrievalJob: %v", err)
	}

	count, err := NewWorker(database).RunOnce()
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	summaries, err := database.FreshExternalContextSummariesByProject("api-service", 5)
	if err != nil {
		t.Fatalf("FreshExternalContextSummariesByProject: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("len(summaries) = %d, want 1", len(summaries))
	}
	if summaries[0].Source != "context7" {
		t.Fatalf("Source = %q, want context7", summaries[0].Source)
	}
	if summaries[0].CacheRef != "/facebook/react" {
		t.Fatalf("CacheRef = %q, want /facebook/react", summaries[0].CacheRef)
	}
	if !strings.Contains(summaries[0].Narrative, "Hooks let you use state and lifecycle features") {
		t.Fatalf("unexpected narrative %q", summaries[0].Narrative)
	}
	if summaries[0].Hint != "Hooks let you use state and lifecycle features from function components." {
		t.Fatalf("Hint = %q, want compact official hint", summaries[0].Hint)
	}

	jobs, err := database.RetrievalJobsByProject("api-service", 5)
	if err != nil {
		t.Fatalf("RetrievalJobsByProject: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Status != "done" {
		t.Fatalf("expected done job, got %#v", jobs)
	}
}

func TestWorkerRunOnce_StoresContext7SetupSummary(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	server := context7SetupTestServer()
	defer server.Close()
	t.Setenv("FORGE_CONTEXT7_SETUP_DOCS_BASE", server.URL)

	if err := database.InsertRetrievalJob(&db.RetrievalJob{
		ProjectID:   "api-service",
		TriggerType: "prompt",
		Source:      "context7_setup",
		Query:       "context7 local setup for codex",
		LibraryName: "context7",
		Status:      "queued",
		Priority:    95,
		DedupeKey:   "job-setup-1",
	}); err != nil {
		t.Fatalf("InsertRetrievalJob: %v", err)
	}

	count, err := NewWorker(database).RunOnce()
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	summaries, err := database.FreshExternalContextSummariesByProject("api-service", 5)
	if err != nil {
		t.Fatalf("FreshExternalContextSummariesByProject: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("len(summaries) = %d, want 1", len(summaries))
	}
	if summaries[0].Source != "context7_setup" {
		t.Fatalf("Source = %q, want context7_setup", summaries[0].Source)
	}
	if !strings.Contains(summaries[0].Narrative, "OpenAI Codex") || !strings.Contains(summaries[0].Narrative, "@upstash/context7-mcp") {
		t.Fatalf("unexpected narrative %q", summaries[0].Narrative)
	}
}

func TestWorkerRunOnce_MarksContext7JobFailedWhenCommandMissing(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	t.Setenv("FORGE_CONTEXT7_MCP_CMD", "missing-context7-mcp-binary")
	t.Setenv("FORGE_CONTEXT7_MCP_ARGS", "")

	if err := database.InsertRetrievalJob(&db.RetrievalJob{
		ProjectID:   "api-service",
		TriggerType: "prompt",
		Source:      "context7",
		Query:       "How do I use React hooks correctly?",
		LibraryName: "react",
		Status:      "queued",
		Priority:    90,
		DedupeKey:   "job-mcp-2",
	}); err != nil {
		t.Fatalf("InsertRetrievalJob: %v", err)
	}

	count, err := NewWorker(database).RunOnce()
	if err == nil {
		t.Fatal("expected RunOnce error for missing context7 mcp command")
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}

	jobs, err := database.RetrievalJobsByProject("api-service", 5)
	if err != nil {
		t.Fatalf("RetrievalJobsByProject: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Status != "failed" {
		t.Fatalf("expected failed job, got %#v", jobs)
	}
}

func TestContext7LibraryIDFromQuery(t *testing.T) {
	got := context7LibraryIDFromQuery("Use library /supabase/supabase for auth docs")
	if got != "/supabase/supabase" {
		t.Fatalf("context7LibraryIDFromQuery = %q, want /supabase/supabase", got)
	}
}

func TestContext7LibraryIDFromQuery_IgnoresFilesystemPath(t *testing.T) {
	got := context7LibraryIDFromQuery("working directory /tmp/api-service while fixing rust build")
	if got != "" {
		t.Fatalf("context7LibraryIDFromQuery = %q, want empty string for filesystem path", got)
	}
}

func TestExtractContext7LibraryID_ParsesStructuredTextJSON(t *testing.T) {
	resp := map[string]any{
		"result": map[string]any{
			"content": []any{
				map[string]any{
					"type": "text",
					"text": `[{"libraryId":"/vercel/next.js","title":"Next.js"}]`,
				},
			},
		},
	}

	got := extractContext7LibraryID(resp)
	if got != "/vercel/next.js" {
		t.Fatalf("extractContext7LibraryID = %q, want /vercel/next.js", got)
	}
}

func TestSummarizeContext7DocsResponse_DistillsSingleOfficialHint(t *testing.T) {
	resp := map[string]any{
		"result": map[string]any{
			"content": []any{
				map[string]any{
					"type": "text",
					"text": `[{"title":"Incorrect React Hook Usage Violating Rules of Hooks","content":"This snippet illustrates various scenarios where React Hooks are used incorrectly, leading to errors. Hooks must not be called inside conditions, loops, after conditional returns, within event handlers, inside useMemo or useEffect callbacks, or within class components. Source: https://github.com/reactjs/react.dev/blob/main/src/content/warnings/invalid-hook-call-warning.md"}]`,
				},
			},
		},
	}

	hint, narrative := summarizeContext7DocsResponse(&db.RetrievalJob{
		TriggerType: "failure",
		Source:      "context7",
		Query:       "react invalid hook call official docs explanation and fix in one short sentence",
		LibraryName: "react",
	}, resp)
	want := "Hooks must not be called inside conditions, loops, after conditional returns, within event handlers, inside useMemo or useEffect callbacks, or within class components."
	if hint != want {
		t.Fatalf("hint = %q, want %q", hint, want)
	}
	if !strings.Contains(narrative, "Hooks must not be called inside conditions") {
		t.Fatalf("unexpected narrative %q", narrative)
	}
}

func TestMaybeRefineContext7Hint_UsesConfiguredAIProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{
				{"text": `{"relevant":true,"confidence":0.94,"hint":"Import the trait that provides the missing method before calling it."}`},
			},
		})
	}))
	defer server.Close()

	t.Setenv("HOME", t.TempDir())
	t.Setenv("FORGE_PROVIDER", "anthropic")
	t.Setenv("FORGE_API_KEY", "sk-ant-test")
	t.Setenv("FORGE_BASE_URL", server.URL)

	job := &db.RetrievalJob{
		TriggerType: "failure",
		Source:      "context7",
		Query:       "rust cargo E0599 no method named serve found official docs explanation and fix in one short sentence",
		LibraryName: "rust",
	}
	refined, used, err := maybeRefineContext7Hint(job, "Rust E0599 usually means the method is not in scope.", []string{
		"Rust E0599 usually means the method is not in scope or the trait providing it is not imported.",
		"Import the trait or call a method that exists for the type.",
	})
	if err != nil {
		t.Fatalf("maybeRefineContext7Hint: %v", err)
	}
	if !used {
		t.Fatal("expected AI refinement to be applied")
	}
	want := "Import the trait that provides the missing method before calling it."
	if refined != want {
		t.Fatalf("refined = %q, want %q", refined, want)
	}
}

func TestContext7MCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CONTEXT7_MCP_HELPER") != "1" {
		return
	}

	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer writer.Flush()

	for {
		msg, err := readMCPTestMessage(reader)
		if err != nil {
			if err == io.EOF {
				os.Exit(0)
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		method, _ := msg["method"].(string)
		switch method {
		case "initialize":
			writeMCPTestMessage(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      msg["id"],
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "context7-test", "version": "1.0.0"},
				},
			})
		case "notifications/initialized":
			continue
		case "tools/list":
			writeMCPTestMessage(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      msg["id"],
				"result": map[string]any{
					"tools": []any{
						map[string]any{"name": "resolve-library-id"},
						map[string]any{"name": "query-docs"},
					},
				},
			})
		case "tools/call":
			params, _ := msg["params"].(map[string]any)
			name, _ := params["name"].(string)
			switch name {
			case "resolve-library-id":
				writeMCPTestMessage(writer, map[string]any{
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"result": map[string]any{
						"content": []any{
							map[string]any{
								"type": "text",
								"text": `[{"libraryId":"/facebook/react","title":"React"}]`,
							},
						},
					},
				})
			case "query-docs":
				writeMCPTestMessage(writer, map[string]any{
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"result": map[string]any{
						"content": []any{
							map[string]any{
								"type": "text",
								"text": `[{"title":"Hooks","content":"Hooks let you use state and lifecycle features from function components. Start with useState and useEffect for local state and side effects."}]`,
							},
						},
					},
				})
			default:
				writeMCPTestMessage(writer, map[string]any{
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"error":   map[string]any{"code": -32601, "message": "unknown tool"},
				})
			}
		default:
			writeMCPTestMessage(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      msg["id"],
				"error":   map[string]any{"code": -32601, "message": "unknown method"},
			})
		}
	}
}

func readMCPTestMessage(reader *bufio.Reader) (map[string]any, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	body := []byte(strings.TrimRight(line, "\r\n"))
	var msg map[string]any
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func writeMCPTestMessage(writer *bufio.Writer, msg map[string]any) {
	body, _ := json.Marshal(msg)
	writer.Write(body)
	writer.WriteByte('\n')
	writer.Flush()
}

func TestWorkerRunOnce_StoresExaSummary(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-exa-key" {
			t.Fatalf("unexpected api key %q", r.Header.Get("x-api-key"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []any{
				map[string]any{
					"title": "Rust async trait objects in 2025",
					"url":   "https://blog.example.com/rust-async-trait",
					"text":  "Use the async-trait crate or the built-in async fn in trait syntax stabilized in Rust 1.75. For object-safe async traits, prefer returning Pin<Box<dyn Future>>.",
					"score": 0.91,
				},
				map[string]any{
					"title": "Rust trait object pitfalls",
					"url":   "https://other.example.com/rust-traits",
					"text":  "Dynamic dispatch with dyn Trait requires object safety. Async fn in traits was not object-safe before Rust 1.75.",
					"score": 0.82,
				},
			},
		})
	}))
	defer server.Close()

	t.Setenv("FORGE_EXA_API_KEY", "test-exa-key")
	t.Setenv("FORGE_EXA_BASE_URL", server.URL)

	if err := database.InsertRetrievalJob(&db.RetrievalJob{
		ProjectID:   "api-service",
		TriggerType: "failure",
		Source:      "exa",
		Query:       "rust async trait object fix 2025",
		LibraryName: "rust",
		Status:      "queued",
		Priority:    85,
		DedupeKey:   "job-exa-1",
	}); err != nil {
		t.Fatalf("InsertRetrievalJob: %v", err)
	}

	count, err := NewWorker(database).RunOnce()
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	summaries, err := database.FreshExternalContextSummariesByProject("api-service", 5)
	if err != nil {
		t.Fatalf("FreshExternalContextSummariesByProject: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("len(summaries) = %d, want 1", len(summaries))
	}
	if summaries[0].Source != "exa" {
		t.Fatalf("Source = %q, want exa", summaries[0].Source)
	}
	if summaries[0].SummaryKind != "web_hint" && summaries[0].SummaryKind != "web_hint_ai" {
		t.Fatalf("SummaryKind = %q, want web_hint or web_hint_ai", summaries[0].SummaryKind)
	}
	if !strings.Contains(summaries[0].Narrative, "async") {
		t.Fatalf("unexpected narrative %q", summaries[0].Narrative)
	}
	if summaries[0].TrustScore != 0.68 {
		t.Fatalf("TrustScore = %v, want 0.68", summaries[0].TrustScore)
	}
}

func TestWorkerRunOnce_StoresTavilySummary(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answer": "In Next.js 14+, use the App Router with server components for data fetching instead of getServerSideProps.",
			"results": []any{
				map[string]any{
					"title":   "Next.js App Router migration guide",
					"url":     "https://nextjs.org/docs/app/building-your-application/upgrading/app-router-migration",
					"content": "The App Router uses React Server Components by default. Migrate data fetching from getServerSideProps to async server components.",
					"score":   0.88,
				},
			},
		})
	}))
	defer server.Close()

	t.Setenv("FORGE_TAVILY_API_KEY", "test-tavily-key")
	t.Setenv("FORGE_TAVILY_BASE_URL", server.URL)

	if err := database.InsertRetrievalJob(&db.RetrievalJob{
		ProjectID:   "web-app",
		TriggerType: "prompt",
		Source:      "tavily",
		Query:       "next.js data fetching best practice 2025",
		LibraryName: "next.js",
		Status:      "queued",
		Priority:    70,
		DedupeKey:   "job-tavily-1",
	}); err != nil {
		t.Fatalf("InsertRetrievalJob: %v", err)
	}

	count, err := NewWorker(database).RunOnce()
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	summaries, err := database.FreshExternalContextSummariesByProject("web-app", 5)
	if err != nil {
		t.Fatalf("FreshExternalContextSummariesByProject: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("len(summaries) = %d, want 1", len(summaries))
	}
	if summaries[0].Source != "tavily" {
		t.Fatalf("Source = %q, want tavily", summaries[0].Source)
	}
	if summaries[0].SummaryKind != "web_summary" {
		t.Fatalf("SummaryKind = %q, want web_summary", summaries[0].SummaryKind)
	}
	if !strings.Contains(summaries[0].Narrative, "App Router") && !strings.Contains(summaries[0].Narrative, "server components") {
		t.Fatalf("unexpected narrative %q", summaries[0].Narrative)
	}
}

func TestWorkerRunOnce_ExaFailsWithoutAPIKey(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	t.Setenv("FORGE_EXA_API_KEY", "")
	t.Setenv("EXA_API_KEY", "")
	t.Setenv("HOME", t.TempDir()) // empty config dir

	if err := database.InsertRetrievalJob(&db.RetrievalJob{
		ProjectID:   "api-service",
		TriggerType: "failure",
		Source:      "exa",
		Query:       "rust fix 2025",
		LibraryName: "rust",
		Status:      "queued",
		Priority:    85,
		DedupeKey:   "job-exa-nokey",
	}); err != nil {
		t.Fatalf("InsertRetrievalJob: %v", err)
	}

	count, err := NewWorker(database).RunOnce()
	if err == nil {
		t.Fatal("expected error when no Exa API key configured")
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	if !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("unexpected error %q", err.Error())
	}
}

func context7SetupTestServer() *httptest.Server {
	handler := http.NewServeMux()
	handler.HandleFunc("/docs/installation", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`
			<html><body>
			<h1>Installation - Context7 MCP</h1>
			<h2>Recommended Setup</h2>
			<p>Remote Server (HTTP) - Easier setup with no local dependencies</p>
			<h2>OpenAI Codex</h2>
			<p>Local Server:</p>
			<pre>[mcp_servers.context7]
command = "npx"
args = ["-y", "@upstash/context7-mcp", "--api-key", "YOUR_API_KEY"]</pre>
			<h2>Claude Code</h2>
			<p>Local Server:</p>
			<pre>claude mcp add context7 -- npx -y @upstash/context7-mcp --api-key YOUR_API_KEY</pre>
			<h2>Gemini CLI</h2>
			<p>Local Server:</p>
			<pre>{"mcpServers":{"context7":{"command":"npx","args":["-y","@upstash/context7-mcp","--api-key","YOUR_API_KEY"]}}}</pre>
			</body></html>
		`))
	})
	handler.HandleFunc("/docs/clients/cli", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`
			<html><body>
			<h1>CLI</h1>
			<p>Use Context7 tools resolve-library-id and query-docs from your client once MCP is configured.</p>
			</body></html>
		`))
	})
	return httptest.NewServer(handler)
}
