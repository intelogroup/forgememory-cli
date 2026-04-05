package retrieve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/forge/forge/internal/db"
)

type context7MCPClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr bytes.Buffer
	nextID int
}

func newContext7MCPClient(ctx context.Context) (*context7MCPClient, error) {
	cmdName, args := context7MCPCommand()
	cmd := exec.CommandContext(ctx, cmdName, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("context7 mcp stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("context7 mcp stdout: %w", err)
	}

	client := &context7MCPClient{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
		nextID: 1,
	}
	cmd.Stderr = &client.stderr
	if err := cmd.Start(); err != nil {
		return nil, context7MCPErrorf(client, "start", err)
	}
	return client, nil
}

func (c *context7MCPClient) Close() error {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	_ = c.stdin.Close()
	err := c.cmd.Wait()
	if err == nil {
		return nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == -1 {
		return nil
	}
	return context7MCPErrorf(c, "wait", err)
}

func (c *context7MCPClient) Shutdown() {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return
	}
	_ = c.stdin.Close()
	_ = c.cmd.Process.Kill()
	_, _ = io.Copy(io.Discard, c.stdout)
	_ = c.cmd.Wait()
}

func (c *context7MCPClient) Initialize() error {
	if _, err := c.request("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "forge",
			"version": "0.1.0",
		},
	}); err != nil {
		return err
	}
	return c.notify("notifications/initialized", map[string]any{})
}

func (c *context7MCPClient) ListTools() ([]string, error) {
	resp, err := c.request("tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	result, _ := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		toolMap, _ := tool.(map[string]any)
		name, _ := toolMap["name"].(string)
		if name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("context7 mcp returned no tools")
	}
	return names, nil
}

func (c *context7MCPClient) CallTool(name string, args map[string]any) (map[string]any, error) {
	return c.request("tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
}

func (c *context7MCPClient) notify(method string, params map[string]any) error {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	return c.write(payload)
}

func (c *context7MCPClient) request(method string, params map[string]any) (map[string]any, error) {
	id := c.nextID
	c.nextID++
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := c.write(payload); err != nil {
		return nil, err
	}

	for {
		msg, err := c.readMessage()
		if err != nil {
			return nil, err
		}
		if _, ok := msg["id"]; !ok {
			continue
		}
		gotID, ok := numericID(msg["id"])
		if !ok || gotID != id {
			continue
		}
		if errObj, ok := msg["error"].(map[string]any); ok {
			return nil, fmt.Errorf("context7 mcp %s: %s", method, firstNonEmpty(anyString(errObj["message"]), compactText(c.stderr.String())))
		}
		return msg, nil
	}
}

func (c *context7MCPClient) write(payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("context7 mcp marshal: %w", err)
	}
	body = append(body, '\n')
	if _, err := c.stdin.Write(body); err != nil {
		return context7MCPErrorf(c, "write body", err)
	}
	return nil
}

func (c *context7MCPClient) readMessage() (map[string]any, error) {
	for {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			return nil, context7MCPErrorf(c, "read message", err)
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(trimmed), "content-length:") {
			var msg map[string]any
			if err := json.Unmarshal([]byte(trimmed), &msg); err != nil {
				return nil, fmt.Errorf("context7 mcp decode line: %w", err)
			}
			return msg, nil
		}

		headers := map[string]string{}
		parts := strings.SplitN(trimmed, ":", 2)
		headers[strings.ToLower(strings.TrimSpace(parts[0]))] = strings.TrimSpace(parts[1])
		for {
			headerLine, err := c.stdout.ReadString('\n')
			if err != nil {
				return nil, context7MCPErrorf(c, "read header", err)
			}
			headerLine = strings.TrimRight(headerLine, "\r\n")
			if headerLine == "" {
				break
			}
			headerParts := strings.SplitN(headerLine, ":", 2)
			if len(headerParts) != 2 {
				return nil, fmt.Errorf("context7 mcp malformed header %q", headerLine)
			}
			headers[strings.ToLower(strings.TrimSpace(headerParts[0]))] = strings.TrimSpace(headerParts[1])
		}

		lengthValue := headers["content-length"]
		if lengthValue == "" {
			return nil, fmt.Errorf("context7 mcp missing content-length header")
		}
		length, err := strconv.Atoi(lengthValue)
		if err != nil || length < 0 {
			return nil, fmt.Errorf("context7 mcp invalid content-length %q", lengthValue)
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(c.stdout, body); err != nil {
			return nil, context7MCPErrorf(c, "read body", err)
		}

		var msg map[string]any
		if err := json.Unmarshal(body, &msg); err != nil {
			return nil, fmt.Errorf("context7 mcp decode body: %w", err)
		}
		return msg, nil
	}
}

func context7MCPCommand() (string, []string) {
	if cmd := strings.TrimSpace(os.Getenv("FORGE_CONTEXT7_MCP_CMD")); cmd != "" {
		return cmd, strings.Fields(os.Getenv("FORGE_CONTEXT7_MCP_ARGS"))
	}
	args := []string{"-y", "@upstash/context7-mcp@latest", "--transport", "stdio"}
	if apiKey := strings.TrimSpace(os.Getenv("CONTEXT7_API_KEY")); apiKey != "" {
		args = append(args, "--api-key", apiKey)
	}
	return "npx", args
}

func (w *Worker) fetchContext7MCP(job *db.RetrievalJob) (*db.ExternalContextSummary, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := newContext7MCPClient(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Shutdown()

	if err := client.Initialize(); err != nil {
		return nil, err
	}

	toolNames, err := client.ListTools()
	if err != nil {
		return nil, err
	}
	resolveTool := context7ToolName(toolNames, "resolve-library-id", "resolveLibraryId")
	docsTool := context7ToolName(toolNames, "query-docs", "queryDocs", "get-library-docs", "getLibraryDocs")
	if docsTool == "" {
		return nil, fmt.Errorf("context7 mcp missing docs tool in %v", toolNames)
	}

	libraryID := directContext7LibraryID(job)
	if libraryID == "" {
		if resolveTool == "" {
			return nil, fmt.Errorf("context7 mcp missing resolve tool in %v", toolNames)
		}
		resolveResp, err := client.CallTool(resolveTool, map[string]any{
			"query":       context7Query(job),
			"libraryName": context7LibraryName(job),
		})
		if err != nil {
			return nil, err
		}
		libraryID = extractContext7LibraryID(resolveResp)
		if libraryID == "" {
			return nil, fmt.Errorf("context7 mcp did not resolve a library id for %q", context7LibraryName(job))
		}
	}

	docsResp, err := client.CallTool(docsTool, map[string]any{
		"libraryId": libraryID,
		"query":     context7Query(job),
	})
	if err != nil {
		return nil, err
	}
	parts := extractContext7Texts(docsResp)
	hint, narrative := summarizeContext7DocsResponse(job, docsResp)
	if narrative == "" {
		return nil, fmt.Errorf("context7 mcp returned empty docs for %s", libraryID)
	}
	refinedHint, aiRefined, _ := maybeRefineContext7Hint(job, hint, parts)
	if aiRefined {
		hint = refinedHint
	}
	kind := aiSummaryKind(summaryKind(job), aiRefined)
	tags := aiSummaryTags(summaryTags(job), aiRefined)

	return &db.ExternalContextSummary{
		TS:             time.Now().UTC().Format(time.RFC3339),
		Source:         job.Source,
		ProjectID:      job.ProjectID,
		Query:          job.Query,
		LibraryName:    context7SummaryLibraryName(job, libraryID),
		LibraryVersion: job.LibraryVersion,
		Title:          summaryTitle(job),
		Narrative:      truncate(narrative, 420),
		SummaryKind:    kind,
		Hint:           truncate(hint, 220),
		TrustScore:     trustScore(job.Source),
		RelevanceTags:  tags,
		CacheRef:       libraryID,
		ExpiresAt:      time.Now().UTC().Add(summaryTTL(job.Source)).Format(time.RFC3339),
	}, nil
}

func context7ToolName(names []string, candidates ...string) string {
	for _, candidate := range candidates {
		for _, name := range names {
			if name == candidate {
				return name
			}
		}
	}
	return ""
}

func context7LibraryName(job *db.RetrievalJob) string {
	return firstNonEmpty(job.LibraryName, context7LibraryIDFromQuery(job.Query))
}

func context7Query(job *db.RetrievalJob) string {
	query := compactQuery(job.Query)
	if query == "" {
		return firstNonEmpty(job.LibraryName, "library documentation")
	}
	return query
}

var context7LibraryIDPattern = regexp.MustCompile(`/(?:[A-Za-z0-9._-]+/)+[A-Za-z0-9._@-]+`)

var context7PathRootBlocklist = map[string]bool{
	"app": true, "applications": true, "bin": true, "dev": true, "etc": true,
	"home": true, "lib": true, "lib64": true, "media": true, "mnt": true,
	"opt": true, "private": true, "proc": true, "root": true, "run": true,
	"sbin": true, "srv": true, "sys": true, "tmp": true, "users": true,
	"usr": true, "var": true, "volumes": true,
}

func context7LibraryIDFromQuery(query string) string {
	for _, match := range context7LibraryIDPattern.FindAllString(query, -1) {
		if value := normalizeContext7LibraryID(match); value != "" {
			return value
		}
	}
	return ""
}

func directContext7LibraryID(job *db.RetrievalJob) string {
	if job == nil {
		return ""
	}
	if value := normalizeContext7LibraryID(job.LibraryVersion); value != "" {
		return value
	}
	if value := normalizeContext7LibraryID(job.LibraryName); value != "" {
		return value
	}
	if strings.TrimSpace(job.LibraryName) == "" {
		return context7LibraryIDFromQuery(job.Query)
	}
	return ""
}

func normalizeContext7LibraryID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	match := strings.TrimSpace(context7LibraryIDPattern.FindString(value))
	if match == "" {
		return ""
	}
	trimmed := strings.TrimPrefix(match, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 2 {
		return ""
	}
	if context7PathRootBlocklist[strings.ToLower(parts[0])] {
		return ""
	}
	return "/" + trimmed
}

func extractContext7LibraryID(resp map[string]any) string {
	if value := extractContext7LibraryIDFromValue(resp); value != "" {
		return value
	}
	for _, text := range extractContext7Texts(resp) {
		if value := context7LibraryIDFromQuery(text); value != "" {
			return value
		}
	}
	return ""
}

func extractContext7LibraryIDFromValue(value any) string {
	switch v := value.(type) {
	case map[string]any:
		for _, key := range []string{"libraryId", "context7CompatibleLibraryID", "id"} {
			if text := normalizeContext7LibraryID(anyString(v[key])); text != "" {
				return text
			}
		}
		for _, child := range v {
			if text := extractContext7LibraryIDFromValue(child); text != "" {
				return text
			}
		}
	case []any:
		for _, child := range v {
			if text := extractContext7LibraryIDFromValue(child); text != "" {
				return text
			}
		}
	case string:
		trimmed := strings.TrimSpace(v)
		if text := normalizeContext7LibraryID(trimmed); text != "" {
			return text
		}
		var parsed any
		if json.Unmarshal([]byte(trimmed), &parsed) == nil {
			return extractContext7LibraryIDFromValue(parsed)
		}
	}
	return ""
}

func summarizeContext7DocsResponse(job *db.RetrievalJob, resp map[string]any) (string, string) {
	parts := extractContext7Texts(resp)
	if len(parts) == 0 {
		return "", ""
	}

	candidates := context7SentenceCandidates(parts)
	best := bestContext7Sentence(job, candidates)
	narrative := compactContext7Narrative(parts)
	if best != "" {
		return best, narrative
	}

	seen := make(map[string]bool)
	for _, part := range parts {
		part = cleanContext7Sentence(part)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		best = ensureSentence(part)
		break
	}
	if narrative == "" {
		narrative = best
	}
	return best, narrative
}

func extractContext7Texts(value any) []string {
	var out []string
	collectContext7Texts(value, &out)
	return out
}

func collectContext7Texts(value any, out *[]string) {
	switch v := value.(type) {
	case map[string]any:
		for _, key := range []string{"title", "content", "text", "description", "summary"} {
			appendContext7Text(v[key], out)
		}
		for key, child := range v {
			if key == "title" || key == "content" || key == "text" || key == "description" || key == "summary" {
				continue
			}
			collectContext7Texts(child, out)
		}
	case []any:
		for _, child := range v {
			collectContext7Texts(child, out)
		}
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return
		}
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var parsed any
			if json.Unmarshal([]byte(trimmed), &parsed) == nil {
				collectContext7Texts(parsed, out)
				return
			}
		}
		*out = append(*out, trimmed)
	}
}

func appendContext7Text(value any, out *[]string) {
	switch v := value.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return
		}
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var parsed any
			if json.Unmarshal([]byte(trimmed), &parsed) == nil {
				collectContext7Texts(parsed, out)
				return
			}
		}
		*out = append(*out, trimmed)
	case []any:
		for _, child := range v {
			appendContext7Text(child, out)
		}
	case map[string]any:
		collectContext7Texts(v, out)
	}
}

func context7SentenceCandidates(parts []string) []string {
	seen := make(map[string]bool)
	var candidates []string
	for _, part := range parts {
		part = strings.NewReplacer(
			"\r", " ",
			"\n", ". ",
			"#", " ",
			"*", " ",
			"`", "",
			"!", ". ",
			"?", ". ",
		).Replace(part)
		part = compactText(part)
		if part == "" {
			continue
		}
		for _, fragment := range strings.Split(part, ". ") {
			sentence := cleanContext7Sentence(fragment)
			if sentence == "" || seen[sentence] {
				continue
			}
			seen[sentence] = true
			candidates = append(candidates, sentence)
		}
	}
	return candidates
}

func cleanContext7Sentence(text string) string {
	text = compactText(text)
	if text == "" {
		return ""
	}
	if idx := strings.Index(text, " Source: "); idx >= 0 {
		text = strings.TrimSpace(text[:idx])
	}
	text = strings.Trim(text, " -:;,.")
	lowered := strings.ToLower(text)
	switch {
	case strings.HasPrefix(lowered, "source:"),
		strings.HasPrefix(lowered, "http://"),
		strings.HasPrefix(lowered, "https://"),
		strings.Contains(lowered, "github.com/"),
		strings.Contains(lowered, "blob/main"),
		strings.Contains(lowered, "```"),
		strings.Contains(lowered, "available libraries"),
		strings.Contains(lowered, "code snippets"),
		strings.Contains(lowered, "benchmark score"):
		return ""
	case strings.HasPrefix(lowered, "this snippet"),
		strings.HasPrefix(lowered, "this example"),
		strings.HasPrefix(lowered, "example:"):
		return ""
	}
	if countMeaningfulWords(text) < 5 {
		return ""
	}
	return text
}

func bestContext7Sentence(job *db.RetrievalJob, candidates []string) string {
	bestScore := -1
	best := ""
	queryTokens := tokenize(firstNonEmpty(job.Query, job.LibraryName))
	for _, candidate := range candidates {
		score := context7SentenceScore(candidate)
		lowered := strings.ToLower(candidate)
		for _, token := range queryTokens {
			if len(token) < 3 {
				continue
			}
			if strings.Contains(lowered, token) {
				score++
			}
		}
		if score > bestScore {
			bestScore = score
			best = candidate
		}
	}
	if best == "" {
		return ""
	}
	return truncate(ensureSentence(best), 220)
}

func compactContext7Narrative(parts []string) string {
	candidates := context7SentenceCandidates(parts)
	sort.SliceStable(candidates, func(i, j int) bool {
		return context7SentenceScore(candidates[i]) > context7SentenceScore(candidates[j])
	})
	uniq := make([]string, 0, 3)
	for _, candidate := range candidates {
		uniq = append(uniq, ensureSentence(candidate))
		if len(uniq) == 3 {
			break
		}
	}
	return strings.Join(uniq, " ")
}

func context7SentenceScore(candidate string) int {
	score := 0
	lowered := strings.ToLower(candidate)
	for _, term := range []string{"must", "must not", "should", "should not", "cannot", "can't", "top level", "not in scope", "import", "use", "inside", "before"} {
		if strings.Contains(lowered, term) {
			score += 2
		}
	}
	for _, bad := range []string{"available libraries", "source reputation", "benchmark score", "documentation not found"} {
		if strings.Contains(lowered, bad) {
			score -= 4
		}
	}
	if len(strings.Fields(candidate)) > 34 {
		score--
	}
	if !strings.ContainsAny(candidate, ".!?") {
		score--
	}
	return score
}

func ensureSentence(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	last := text[len(text)-1]
	if last != '.' && last != '!' && last != '?' {
		text += "."
	}
	return text
}

func countMeaningfulWords(text string) int {
	count := 0
	for _, word := range strings.Fields(text) {
		word = strings.TrimFunc(word, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		if len(word) >= 3 {
			count++
		}
	}
	return count
}

func anyString(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

func numericID(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case string:
		n, err := strconv.Atoi(v)
		return n, err == nil
	default:
		return 0, false
	}
}

func context7SummaryLibraryName(job *db.RetrievalJob, libraryID string) string {
	if job.LibraryName != "" && job.LibraryName != "context7" {
		return job.LibraryName
	}
	trimmed := strings.TrimPrefix(libraryID, "/")
	if trimmed == "" {
		return job.LibraryName
	}
	parts := strings.Split(trimmed, "/")
	return parts[len(parts)-1]
}

func context7MCPErrorf(client *context7MCPClient, action string, err error) error {
	msg := ""
	if client != nil {
		msg = compactText(client.stderr.String())
	}
	if msg == "" {
		return fmt.Errorf("context7 mcp %s: %w", action, err)
	}
	return fmt.Errorf("context7 mcp %s: %v (%s)", action, err, msg)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
