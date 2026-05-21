package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Client is a Go MCP client that spawns forge mcp as a subprocess
// and communicates with it via JSON-RPC 2.0 over stdio.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex
	nextID int
	close  sync.Once
}

// NewClient spawns forge mcp and performs the initialize handshake.
// Returns an error if the binary is not found or the handshake fails.
func NewClient() (*Client, error) {
	binary := os.Args[0]
	cmd := exec.Command(binary, "mcp")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp client: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("mcp client: stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("mcp client: start: %w", err)
	}

	c := &Client{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
		nextID: 1,
	}

	if err := c.handshake(); err != nil {
		c.Close()
		return nil, err
	}

	return c, nil
}

func (c *Client) handshake() error {
	done := make(chan error, 1)
	go func() {
		_, err := c.sendRequest("initialize", map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "forge-distill-agent",
				"version": "0.1.0",
			},
		})
		if err != nil {
			done <- fmt.Errorf("mcp handshake: %w", err)
			return
		}
		c.sendNotification("notifications/initialized", nil)
		done <- nil
	}()

	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		return fmt.Errorf("mcp handshake: timeout after 10s")
	}
}

func (c *Client) sendRequest(method string, params map[string]any) (json.RawMessage, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.mu.Unlock()

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("mcp client: marshal request: %w", err)
	}

	if _, err := c.stdin.Write(data); err != nil {
		return nil, fmt.Errorf("mcp client: write request: %w", err)
	}
	if _, err := c.stdin.Write([]byte("\n")); err != nil {
		return nil, fmt.Errorf("mcp client: write newline: %w", err)
	}

	line, err := c.stdout.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("mcp client: read response: %w", err)
	}

	var resp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("mcp client: parse response: %w (raw: %s)", err, string(line))
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("mcp client: %s (code %d)", resp.Error.Message, resp.Error.Code)
	}

	return resp.Result, nil
}

func (c *Client) sendNotification(method string, params map[string]any) {
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}
	data, _ := json.Marshal(req)
	c.stdin.Write(data)
	c.stdin.Write([]byte("\n"))
}

// CallTool calls an MCP tool and returns the text content.
// Multiple content items are concatenated with newlines.
func (c *Client) CallTool(name string, args map[string]any) (string, error) {
	result, err := c.sendRequest("tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return "", err
	}

	var toolResp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &toolResp); err != nil {
		return "", fmt.Errorf("mcp client: parse tool result: %w", err)
	}

	var texts []string
	for _, c := range toolResp.Content {
		if c.Text != "" {
			texts = append(texts, c.Text)
		}
	}
	return joinStrings(texts, "\n"), nil
}

// Close shuts down the MCP subprocess by closing stdin and waiting for exit.
func (c *Client) Close() error {
	var err error
	c.close.Do(func() {
		c.stdin.Close()
		done := make(chan struct{})
		go func() {
			c.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			c.cmd.Process.Kill()
			<-done
		}
	})
	return err
}

func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for _, s := range strs[1:] {
		result += sep + s
	}
	return result
}
