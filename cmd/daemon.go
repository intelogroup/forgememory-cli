package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
	"github.com/forge/forge/internal/ipc"
)

// runDaemon is the entrypoint for `forge daemon`.
func runDaemon(args []string) {
	// Open database
	database, err := db.Open("")
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()

	// Listen for hook events
	ln, addr, err := ipc.Listen()
	if err != nil {
		log.Fatalf("Failed to create IPC listener: %v", err)
	}
	defer ln.Close()

	// Write pipe address and PID for hooks and stop command
	writeAddr(addr)
	writePID(os.Getpid())

	log.Printf("Forge daemon listening on %s (pid %d)", addr, os.Getpid())
	log.Printf("Database: %s", database.Path)

	// Graceful shutdown
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)

	// Accept connections
	var wg sync.WaitGroup
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-shutdown:
					return
				default:
					log.Printf("Accept error: %v", err)
					continue
				}
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				handleConn(conn, database)
			}()
		}
	}()

	// Distillation loop
	distiller := distill.New(database, distill.LoadConfig())
	go distillLoop(distiller)

	<-shutdown
	log.Println("Shutting down...")
	ln.Close()
	wg.Wait()
	cleanAddr()
	cleanPID()
	log.Println("Forge daemon stopped.")
}

func handleConn(conn net.Conn, database *db.DB) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	decoder := json.NewDecoder(conn)

	// Decode into a generic map first to inspect the type field.
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return
	}

	// Determine message type (default: event).
	msgType := "event"
	if t, ok := raw["type"]; ok {
		var s string
		if err := json.Unmarshal(t, &s); err == nil && s != "" {
			msgType = s
		}
	}

	switch msgType {
	case "event":
		handleEventMsg(raw, database)
	// "query" type reserved for future bidirectional IPC
	default:
		handleEventMsg(raw, database)
	}
}

func handleEventMsg(raw map[string]json.RawMessage, database *db.DB) {
	extract := func(key string) string {
		v, ok := raw[key]
		if !ok {
			return ""
		}
		var s string
		json.Unmarshal(v, &s)
		return s
	}

	event := &db.Event{
		ID:         extract("id"),
		TS:         extract("ts"),
		SessionID:  extract("session_id"),
		ProjectID:  extract("project_id"),
		SourceTool: extract("source_tool"),
		EventType:  extract("event_type"),
		ToolName:   extract("tool_name"),
		Payload:    extract("payload"),
	}

	if event.SessionID == "" {
		event.SessionID = "unknown"
	}
	if event.ProjectID == "" {
		event.ProjectID = "unknown"
	}

	if err := database.InsertEvent(event); err != nil {
		log.Printf("Insert event error: %v", err)
	}
}

func distillLoop(d *distill.Distiller) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		count, err := d.DistillBatch(50)
		if err != nil {
			log.Printf("Distillation error: %v", err)
		} else if count > 0 {
			log.Printf("Distilled %d principles from events", count)
		}
	}
}

// forgeHome returns the home directory for forge data files.
// Checks the HOME env var first so that tests can override it via t.Setenv —
// needed on Windows where os.UserHomeDir() ignores HOME and reads USERPROFILE.
func forgeHome() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return home
}

func writeAddr(addr string) {
	home := forgeHome()
	dir := filepath.Join(home, ".forge")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "forge.addr"), []byte(addr), 0o600)
	os.Setenv("FORGE_PIPE_ADDR", addr)
}

func cleanAddr() {
	_ = os.Remove(filepath.Join(forgeHome(), ".forge", "forge.addr"))
}

func readAddr() string {
	data, err := os.ReadFile(filepath.Join(forgeHome(), ".forge", "forge.addr"))
	if err != nil {
		return ""
	}
	return string(data)
}

func writePID(pid int) {
	home := forgeHome()
	dir := filepath.Join(home, ".forge")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "forge.pid"), []byte(fmt.Sprintf("%d", pid)), 0o600)
}

func cleanPID() {
	_ = os.Remove(filepath.Join(forgeHome(), ".forge", "forge.pid"))
}

func readPID() int {
	data, err := os.ReadFile(filepath.Join(forgeHome(), ".forge", "forge.pid"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

// isDaemonAlive checks whether the daemon behind addr is actually responding.
// Returns false for empty addr or if the socket/port can't be dialed.
func isDaemonAlive(addr string) bool {
	if addr == "" {
		return false
	}
	network := "unix"
	if strings.Contains(addr, ":") {
		network = "tcp"
	}
	conn, err := net.DialTimeout(network, addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Status output
func statusOutput() {
	database, err := db.Open("")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer database.Close()

	total, undistilled, _ := database.EventCount()
	principles, _ := database.PrincipleCount()
	sessions, _ := database.SessionSummaryCount()
	addr := readAddr()

	fmt.Printf("Database:  %s\n", database.Path)
	fmt.Printf("Events:    %d (%d undistilled)\n", total, undistilled)
	fmt.Printf("Principles: %d\n", principles)
	fmt.Printf("Sessions:  %d\n", sessions)
	if addr != "" && isDaemonAlive(addr) {
		fmt.Printf("Daemon:    running (%s)\n", addr)
	} else if addr != "" {
		fmt.Printf("Daemon:    stale (%s — not responding)\n", addr)
	} else {
		fmt.Printf("Daemon:    not running\n")
	}
}
