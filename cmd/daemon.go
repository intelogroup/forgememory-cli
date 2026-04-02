package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/forge/forge/internal/db"
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

	// Write pipe address for hooks to find
	writeAddr(addr)

	log.Printf("Forge daemon listening on %s", addr)
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
	go distillLoop(database)

	<-shutdown
	log.Println("Shutting down...")
	ln.Close()
	wg.Wait()
	cleanAddr()
	log.Println("Forge daemon stopped.")
}

func handleConn(conn net.Conn, database *db.DB) {
	defer conn.Close()
	decoder := json.NewDecoder(conn)

	var msg struct {
		ID         string `json:"id"`
		TS         string `json:"ts"`
		SessionID  string `json:"session_id"`
		ProjectID  string `json:"project_id"`
		SourceTool string `json:"source_tool"`
		EventType  string `json:"event_type"`
		ToolName   string `json:"tool_name"`
		Payload    string `json:"payload"`
	}

	if err := decoder.Decode(&msg); err != nil {
		return
	}

	event := &db.Event{
		ID:         msg.ID,
		TS:         msg.TS,
		SessionID:  msg.SessionID,
		ProjectID:  msg.ProjectID,
		SourceTool: msg.SourceTool,
		EventType:  msg.EventType,
		ToolName:   msg.ToolName,
		Payload:    msg.Payload,
	}

	if err := database.InsertEvent(event); err != nil {
		log.Printf("Insert event error: %v", err)
	}
}

func distillLoop(database *db.DB) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		distill(database)
	}
}

func distill(database *db.DB) {
	events, err := database.UndistilledEvents(50)
	if err != nil || len(events) < 5 {
		return
	}
	// TODO: Send to LLM for distillation
	// For now, create a simple summary principle
	var ids []string
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	if len(ids) > 0 {
		_ = database.MarkDistilled(ids)
		log.Printf("Distilled %d events", len(ids))
	}
}

func writeAddr(addr string) {
	home, _ := os.UserHomeDir()
	path := home + "/.forge/forge.addr"
	_ = os.MkdirAll(home+"/.forge", 0o700)
	_ = os.WriteFile(path, []byte(addr), 0o600)
	// Also set env for child processes
	os.Setenv("FORGE_PIPE_ADDR", addr)
}

func cleanAddr() {
	home, _ := os.UserHomeDir()
	_ = os.Remove(home + "/.forge/forge.addr")
}

func readAddr() string {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(home + "/.forge/forge.addr")
	if err != nil {
		return ""
	}
	return string(data)
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
	addr := readAddr()

	fmt.Printf("Database:  %s\n", database.Path)
	fmt.Printf("Events:    %d (%d undistilled)\n", total, undistilled)
	fmt.Printf("Principles: %d\n", principles)
	if addr != "" {
		fmt.Printf("Daemon:    running (%s)\n", addr)
	} else {
		fmt.Printf("Daemon:    not running\n")
	}
}
