// Package git parses local git history into structured commit stats,
// used to link commits back to the agent sessions that produced them.
package git

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CommitStat is one commit's identity plus aggregate line-change counts.
type CommitStat struct {
	SHA        string
	Author     string
	Date       time.Time
	Subject    string
	Files      int
	Insertions int
	Deletions  int
	Paths      []string
}

const commitSep = "@@FORGE@@"

// CommitsSince returns commits in repoPath authored within `since` of now,
// each with aggregate numstat (files/insertions/deletions).
func CommitsSince(repoPath string, since time.Duration) ([]CommitStat, error) {
	sinceArg := fmt.Sprintf("--since=%d.seconds.ago", int(since.Seconds()))
	cmd := exec.Command("git", "-C", repoPath, "log", sinceArg,
		"--numstat", "--date=iso-strict",
		"--pretty=format:"+commitSep+"%H|%an|%ad|%s")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	return parseNumstatLog(&out)
}

func parseNumstatLog(r *bytes.Buffer) ([]CommitStat, error) {
	var commits []CommitStat
	var cur *CommitStat

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, commitSep) {
			if cur != nil {
				commits = append(commits, *cur)
			}
			parts := strings.SplitN(strings.TrimPrefix(line, commitSep), "|", 4)
			if len(parts) != 4 {
				cur = nil
				continue
			}
			ts, err := time.Parse(time.RFC3339, parts[2])
			if err != nil {
				ts = time.Time{}
			}
			cur = &CommitStat{SHA: parts[0], Author: parts[1], Date: ts, Subject: parts[3]}
			continue
		}
		if cur == nil || strings.TrimSpace(line) == "" {
			continue
		}
		// numstat line: "<added>\t<deleted>\t<path>" (binary files use "-")
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 3 {
			continue
		}
		cur.Files++
		cur.Paths = append(cur.Paths, fields[2])
		if n, err := strconv.Atoi(fields[0]); err == nil {
			cur.Insertions += n
		}
		if n, err := strconv.Atoi(fields[1]); err == nil {
			cur.Deletions += n
		}
	}
	if cur != nil {
		commits = append(commits, *cur)
	}
	return commits, scanner.Err()
}
