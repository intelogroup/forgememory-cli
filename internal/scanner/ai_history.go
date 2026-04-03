package scanner

import (
	"crypto/md5"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const (
	hashFile = ".forge/scanner_hashes.json"
)

type hashStore struct {
	Hashes map[string]string `json:"hashes"`
}

type Scanner struct {
	DB *db.DB
}

func loadHashes() (hashStore, error) {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, hashFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return hashStore{Hashes: make(map[string]string)}, nil
	}
	var store hashStore
	if err := json.Unmarshal(data, &store); err != nil {
		return hashStore{Hashes: make(map[string]string)}, err
	}
	if store.Hashes == nil {
		store.Hashes = make(map[string]string)
	}
	return store, nil
}

func saveHashes(store hashStore) error {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".forge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "scanner_hashes.json")
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func hashContent(content string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(content)))
}

func (s *Scanner) scanAIMemories() error {
	home, _ := os.UserHomeDir()

	fmt.Println("\n=== Scanning AI Tool Memories ===")

	hashes, err := loadHashes()
	if err != nil {
		fmt.Printf("  Warning: could not load hash store: %v\n", err)
		hashes = hashStore{Hashes: make(map[string]string)}
	}

	geminiSaved := s.scanGemini(home, hashes)
	codexSaved := s.scanCodex(home, hashes)

	if err := saveHashes(hashes); err != nil {
		fmt.Printf("  Warning: could not save hash store: %v\n", err)
	}

	fmt.Printf("AI memory scan complete: %d Gemini, %d Codex\n", geminiSaved, codexSaved)
	return nil
}

func (s *Scanner) scanGemini(home string, hashes hashStore) int {
	saved := 0
	geminiDir := filepath.Join(home, ".gemini")
	geminiMD := filepath.Join(geminiDir, "GEMINI.md")

	_, err := os.Stat(geminiMD)
	if err != nil {
		return 0
	}

	content, err := os.ReadFile(geminiMD)
	if err != nil || len(content) == 0 {
		return 0
	}

	currentHash := hashContent(string(content))
	key := geminiMD

	if hashes.Hashes[key] == currentHash {
		return 0
	}

	event := &db.Event{
		ID:         uuid.New().String(),
		TS:         time.Now().UTC().Format(time.RFC3339),
		SessionID:  "scanner-gemini",
		ProjectID:  "global",
		SourceTool: "gemini",
		EventType:  "MemoryScan",
		Payload:    fmt.Sprintf(`{"source":"gemini","file":"GEMINI.md","size":%d}`, len(content)),
	}
	if err := s.DB.InsertEvent(event); err != nil {
		fmt.Printf("  Gemini: failed to insert event: %v\n", err)
		return 0
	}

	hashes.Hashes[key] = currentHash
	saved++
	fmt.Printf("  Gemini: scanned GEMINI.md (%d bytes)\n", len(content))
	return saved
}

type codexThread struct {
	ID        string
	CWD       string
	Title     string
	FirstMsg  string
	CreatedAt int
	GitBranch string
}

func (s *Scanner) scanCodex(home string, hashes hashStore) int {
	saved := 0
	codexDir := filepath.Join(home, ".codex")
	dbPath := filepath.Join(codexDir, "state_5.sqlite")

	_, err := os.Stat(dbPath)
	if err != nil {
		return 0
	}

	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Printf("  Codex: could not open DB: %v\n", err)
		return 0
	}
	if err := conn.Ping(); err != nil {
		fmt.Printf("  Codex: could not ping DB: %v\n", err)
		conn.Close()
		return 0
	}
	defer conn.Close()

	cutoff := int(time.Now().Add(-24 * time.Hour).Unix())
	query := fmt.Sprintf(`
		SELECT id, cwd, title, first_user_message, created_at, git_branch
		FROM threads 
		WHERE updated_at >= %d
		ORDER BY updated_at DESC
	`, cutoff)

	rows, err := conn.Query(query)
	if err != nil {
		fmt.Printf("  Codex: query failed: %v\n", err)
		return 0
	}
	defer rows.Close()

	var threads []codexThread
	for rows.Next() {
		var t codexThread
		if err := rows.Scan(&t.ID, &t.CWD, &t.Title, &t.FirstMsg, &t.CreatedAt, &t.GitBranch); err == nil {
			threads = append(threads, t)
		}
	}

	fmt.Printf("  Codex: found %d recent threads\n", len(threads))

	if len(threads) == 0 {
		return 0
	}

	for _, t := range threads {
		threadKey := fmt.Sprintf("codex:thread:%s", t.ID)
		contentHash := hashContent(fmt.Sprintf("%s:%s:%s", t.ID, t.Title, t.FirstMsg))

		if hashes.Hashes[threadKey] == contentHash {
			continue
		}

		project := "unknown"
		if t.CWD != "" {
			project = filepath.Base(t.CWD)
		}

		rollout := s.readCodexRollup(codexDir, t.ID, t.CreatedAt)
		context := fmt.Sprintf("Codex session: %s\nProject: %s\nBranch: %s\nFirst message: %s",
			t.Title, project, t.GitBranch, t.FirstMsg)
		if rollout != "" {
			context += "\n\nSession activity:\n" + rollout
		}

		event := &db.Event{
			ID:         uuid.New().String(),
			TS:         time.Now().UTC().Format(time.RFC3339),
			SessionID:  "scanner-codex",
			ProjectID:  project,
			SourceTool: "codex",
			EventType:  "ThreadScan",
			Payload:    fmt.Sprintf(`{"thread_id":"%s","title":"%s","context_len":%d}`, t.ID, t.Title, len(context)),
		}
		if err := s.DB.InsertEvent(event); err != nil {
			continue
		}

		hashes.Hashes[threadKey] = contentHash
		saved++
		fmt.Printf("  Codex: scanned thread %s (%s)\n", t.ID, t.Title)
	}

	return saved
}

func (s *Scanner) readCodexRollup(codexDir, threadID string, createdAt int) string {
	ts := time.Unix(int64(createdAt), 0)
	dayDir := filepath.Join(codexDir, "sessions",
		fmt.Sprintf("%d/%02d/%02d", ts.Year(), ts.Month(), ts.Day()))

	entries, err := os.ReadDir(dayDir)
	if err != nil {
		return ""
	}

	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > 36 && e.Name()[:36] == threadID && filepath.Ext(e.Name()) == ".jsonl" {
			data, _ := os.ReadFile(filepath.Join(dayDir, e.Name()))
			return string(data)
		}
	}
	return ""
}
