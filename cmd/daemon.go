package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/forge/forge/internal/config"
	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
	"github.com/forge/forge/internal/ipc"
)

// runDaemon is the entrypoint for `forge daemon`.
func runDaemon(args []string) {
	// Load config at startup so daemon has the correct provider settings
	// independent of shell environment.
	cfg, err := config.Load()
	if err == nil {
		// Apply config to environment for distill to pick up
		if cfg.Provider != "" {
			os.Setenv("FORGE_PROVIDER", cfg.Provider)
		}
		if cfg.APIKey != "" {
			os.Setenv("FORGE_API_KEY", cfg.APIKey)
		}
		if cfg.BaseURL != "" {
			os.Setenv("FORGE_API_URL", cfg.BaseURL)
		}
		log.Printf("Loaded config: provider=%s", cfg.Provider)
	} else {
		log.Printf("No config found, using defaults (provider: ollama)")
	}

	// Acquire exclusive lock to prevent multiple daemons
	lockFile, err := acquireDaemonLock()
	if err != nil {
		log.Fatalf("Failed to acquire daemon lock: %v", err)
	}
	if lockFile != nil {
		defer lockFile.Close()
		defer os.Remove(lockFile.Name())
	}

	// Open database
	database, err := db.Open("")
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()

	// Get the parent PID before we daemonize (if started via forge start/mcp)
	// The daemon should exit when its parent dies to avoid orphaned processes.
	// Skip parent monitoring if FORGE_NO_EXIT_ON_PARENT_EXIT is set - this is
	// set by `forge start` so the daemon doesn't exit when the CLI exits.
	parentPID := os.Getppid()
	skipParentMonitor := os.Getenv("FORGE_NO_EXIT_ON_PARENT_EXIT") == "1"

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
	defer signal.Stop(shutdown)

	stop := make(chan struct{})
	var stopOnce sync.Once
	requestStop := func() {
		stopOnce.Do(func() {
			close(stop)
		})
	}

	go func() {
		<-shutdown
		requestStop()
	}()

	// Parent process monitor - exit when parent dies to avoid orphaned daemons.
	// Only monitor if parent is not init (PID 1) - this means we were started
	// by a specific process (like forge mcp) rather than as a system service.
	if parentPID > 1 && !skipParentMonitor {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					// Check if parent is still alive
					proc, err := os.FindProcess(parentPID)
					if err != nil {
						// Parent process gone, exit
						log.Printf("Parent process (pid %d) gone, shutting down", parentPID)
						requestStop()
						_ = ln.Close()
						return
					}
					// On Unix, we need to signal(0) to check if process exists
					if runtime.GOOS != "windows" {
						err := proc.Signal(syscall.Signal(0))
						if err != nil {
							log.Printf("Parent process (pid %d) gone, shutting down", parentPID)
							requestStop()
							_ = ln.Close()
							return
						}
					}
				}
			}
		}()
	}

	// Accept connections
	var wg sync.WaitGroup
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-stop:
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

	<-stop
	log.Println("Shutting down...")
	_ = ln.Close()
	wg.Wait()
	cleanAddr()
	cleanPID()
	cleanLock()
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

// isProcessAlive checks whether a process with the given PID exists.
func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess succeeds even for zombie processes.
	// We use syscall.Signal(0) to check if the process is actually alive.
	if runtime.GOOS != "windows" {
		err := proc.Signal(syscall.Signal(0))
		return err == nil
	}
	// On Windows, FindProcess returns error if process doesn't exist.
	return true
}

// isStaleLock checks whether the lock file exists but points to a dead process.
func isStaleLock() bool {
	lockPath := filepath.Join(forgeHome(), ".forge", "forge.lock")
	_, err := os.Stat(lockPath)
	if err != nil {
		return false
	}
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

// acquireDaemonLock creates an exclusive lock file to ensure only one daemon runs.
// Returns nil, nil if no existing lock, or an error if lock cannot be acquired.
func acquireDaemonLock() (*os.File, error) {
	lockPath := filepath.Join(forgeHome(), ".forge", "forge.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("daemon already running or stale lock exists")
		}
		return nil, fmt.Errorf("acquire lock: %w", err)
	}

	// Write PID to lock file for debugging
	fmt.Fprintf(lockFile, "%d", os.Getpid())
	lockFile.Close()

	// Re-open for holding the lock
	lockFile, err = os.OpenFile(lockPath, os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("reopen lock: %w", err)
	}

	return lockFile, nil
}

// cleanLock removes the daemon lock file.
func cleanLock() {
	lockPath := filepath.Join(forgeHome(), ".forge", "forge.lock")
	_ = os.Remove(lockPath)
}

// cleanSocket removes the daemon socket file.
func cleanSocket() {
	socketPath := filepath.Join(forgeHome(), ".forge", "forge.sock")
	_ = os.Remove(socketPath)
}

// isStaleSocket checks whether the socket file exists.
func isStaleSocket() bool {
	socketPath := filepath.Join(forgeHome(), ".forge", "forge.sock")
	_, err := os.Stat(socketPath)
	return err == nil
}
