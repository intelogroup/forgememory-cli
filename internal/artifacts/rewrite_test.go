package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/sanitize"
)

// rawArtifact writes a file as an older scrubber would have left it.
func rawArtifact(t *testing.T, s Store, id, mediaType, content string) db.EvaluationArtifact {
	t.Helper()
	path, hash, err := s.writeContent([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	a := db.EvaluationArtifact{ID: id, TraceID: "t", Kind: "transcript", MediaType: mediaType, Path: path, SHA256: hash, ByteSize: int64(len(content))}
	if err := s.DB.InsertEvaluationArtifact(&a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRewriteMovesChangedFilesAndKeepsEveryRowReadable(t *testing.T) {
	root := t.TempDir()
	database, err := db.Open(filepath.Join(root, "forge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := Store{DB: database, Root: filepath.Join(root, "artifacts")}
	token := "sbp_" + strings.Repeat("0a1b2c3d", 5)
	shared := "line one " + token + "\n"

	first := rawArtifact(t, s, "a1", "application/jsonl", shared)
	rawArtifact(t, s, "a2", "application/jsonl", shared) // same content, same file
	clean := rawArtifact(t, s, "a3", "text/x-diff", "no secret here\n")
	binary := rawArtifact(t, s, "a4", "application/octet-stream", token)
	gone := rawArtifact(t, s, "a5", "application/json", token+" gone")
	if err := os.Remove(filepath.Join(s.Root, gone.Path)); err != nil {
		t.Fatal(err)
	}

	if n, err := s.Rewrite(sanitize.ScrubSecrets, false); err != nil || n != 1 {
		t.Fatalf("dry run: n=%d err=%v", n, err)
	}
	if _, err := os.Stat(filepath.Join(s.Root, first.Path)); err != nil {
		t.Fatalf("dry run removed the file: %v", err)
	}

	if n, err := s.Rewrite(sanitize.ScrubSecrets, true); err != nil || n != 1 {
		t.Fatalf("apply: n=%d err=%v", n, err)
	}
	if _, err := os.Stat(filepath.Join(s.Root, first.Path)); !os.IsNotExist(err) {
		t.Fatalf("old file still on disk: %v", err)
	}
	for _, id := range []string{"a1", "a2"} {
		a, err := database.EvaluationArtifactByID(id)
		if err != nil {
			t.Fatal(err)
		}
		r, err := s.Open(*a)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(r)
		r.Close()
		sum := sha256.Sum256(body)
		if strings.Contains(string(body), token) || !strings.Contains(string(body), "line one") || a.SHA256 != hex.EncodeToString(sum[:]) || a.ByteSize != int64(len(body)) {
			t.Errorf("%s: body=%q sha=%s size=%d", id, body, a.SHA256, a.ByteSize)
		}
	}
	for id, want := range map[string]string{"a3": clean.Path, "a4": binary.Path} {
		a, err := database.EvaluationArtifactByID(id)
		if err != nil || a.Path != want {
			t.Errorf("%s moved: %+v err=%v", id, a, err)
		}
	}
}
