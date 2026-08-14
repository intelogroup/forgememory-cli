// Package artifacts stores large evaluation evidence outside SQLite while
// keeping ownership and integrity metadata in the database.
package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/sanitize"
	"github.com/google/uuid"
)

var allowedKinds = map[string]bool{
	"transcript":  true,
	"diff":        true,
	"test-report": true,
	"command-log": true,
	"other":       true,
}

// Store writes content-addressed evidence beneath Root and records its index
// row in DB. Root should be private to the Forge user.
type Store struct {
	DB   *db.DB
	Root string
}

// Put stores one evidence object. Textual content is scrubbed before hashing
// so the persisted bytes and their integrity hash describe the redacted copy.
func (s Store) Put(traceID, taskID, kind, mediaType string, content []byte, metadata string) (db.EvaluationArtifact, error) {
	if s.DB == nil {
		return db.EvaluationArtifact{}, fmt.Errorf("artifact store database is required")
	}
	if strings.TrimSpace(traceID) == "" {
		return db.EvaluationArtifact{}, fmt.Errorf("trace ID is required")
	}
	if !allowedKinds[kind] {
		return db.EvaluationArtifact{}, fmt.Errorf("unsupported artifact kind %q", kind)
	}
	if strings.HasPrefix(strings.ToLower(mediaType), "text/") || mediaType == "application/json" || mediaType == "application/xml" {
		content = []byte(sanitize.ScrubSecrets(string(content)))
	}
	if metadata == "" {
		metadata = "{}"
	}
	if s.Root == "" {
		return db.EvaluationArtifact{}, fmt.Errorf("artifact store root is required")
	}

	hashBytes := sha256.Sum256(content)
	hash := hex.EncodeToString(hashBytes[:])
	relativePath := filepath.Join("sha256", hash[:2], hash)
	absolutePath := filepath.Join(s.Root, relativePath)
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o700); err != nil {
		return db.EvaluationArtifact{}, fmt.Errorf("create artifact directory: %w", err)
	}
	if _, err := os.Stat(absolutePath); os.IsNotExist(err) {
		if err := writeAtomic(absolutePath, content); err != nil {
			return db.EvaluationArtifact{}, err
		}
	} else if err != nil {
		return db.EvaluationArtifact{}, fmt.Errorf("check artifact: %w", err)
	}

	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte(strings.Join([]string{traceID, taskID, kind, hash}, "\x00"))).String()
	artifact := db.EvaluationArtifact{
		ID: id, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		TraceID: traceID, TaskID: taskID, Kind: kind, MediaType: mediaType,
		Path: relativePath, SHA256: hash, ByteSize: int64(len(content)), Metadata: metadata,
	}
	if err := s.DB.InsertEvaluationArtifact(&artifact); err != nil {
		return db.EvaluationArtifact{}, fmt.Errorf("insert artifact metadata: %w", err)
	}
	return artifact, nil
}

func writeAtomic(path string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".artifact-*")
	if err != nil {
		return fmt.Errorf("create artifact temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("secure artifact temp file: %w", err)
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write artifact: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close artifact: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			return nil
		}
		return fmt.Errorf("commit artifact: %w", err)
	}
	return nil
}

// Open returns an artifact's content after validating that its indexed path
// remains inside the configured store root.
func (s Store) Open(artifact db.EvaluationArtifact) (io.ReadCloser, error) {
	if s.Root == "" {
		return nil, fmt.Errorf("artifact store root is required")
	}
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return nil, err
	}
	path, err := filepath.Abs(filepath.Join(root, artifact.Path))
	if err != nil {
		return nil, err
	}
	if path != root && !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return nil, fmt.Errorf("artifact path escapes store root")
	}
	return os.Open(path)
}
