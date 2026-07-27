package git

import (
	"bytes"
	"testing"
)

func TestParseNumstatLog(t *testing.T) {
	log := commitSep + "abc123|jimkali|2026-07-18T10:57:29-04:00|fix bug\n" +
		"10\t2\tfoo.go\n" +
		"5\t0\tbar.go\n" +
		commitSep + "def456|jimkali|2026-07-16T17:17:23-04:00|merge\n" +
		commitSep + "ghi789|jimkali|2026-07-16T12:44:58-04:00|binary asset\n" +
		"-\t-\timage.png\n"

	commits, err := parseNumstatLog(bytes.NewBufferString(log))
	if err != nil {
		t.Fatalf("parseNumstatLog: %v", err)
	}
	if len(commits) != 3 {
		t.Fatalf("got %d commits, want 3", len(commits))
	}

	c := commits[0]
	if c.SHA != "abc123" || c.Files != 2 || c.Insertions != 15 || c.Deletions != 2 {
		t.Errorf("commit 0 = %+v, want SHA=abc123 files=2 +15 -2", c)
	}

	merge := commits[1]
	if merge.SHA != "def456" || merge.Files != 0 || merge.Insertions != 0 {
		t.Errorf("merge commit should have zero stats, got %+v", merge)
	}

	binary := commits[2]
	if binary.Files != 1 || binary.Insertions != 0 || binary.Deletions != 0 {
		t.Errorf("binary file (- - path) should count as a touched file with 0 line stats, got %+v", binary)
	}
}
