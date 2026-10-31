package recall

import (
	"fmt"
	"strings"

	"github.com/forge/forge/internal/db"
)

const maxBodyChars = 1500

// Render formats a response as Markdown for agents and terminals. It always
// states the mode, so a caller can tell when meaning-based search did not run.
func Render(resp Response) string {
	var b strings.Builder
	if resp.Mode == ModeFTSOnly {
		fmt.Fprintf(&b, "Mode: %s (%s)\n\n", resp.Mode, resp.Reason)
	} else {
		fmt.Fprintf(&b, "Mode: %s\n\n", resp.Mode)
	}
	if len(resp.Results) == 0 {
		b.WriteString("No matching memories found.\n")
		return b.String()
	}
	for _, r := range resp.Results {
		fmt.Fprintf(&b, "### [%s] %s\n", r.Kind, r.Title)
		date := r.TS
		if len(date) > 10 {
			date = date[:10]
		}
		fmt.Fprintf(&b, "%s | %s", date, r.ProjectID)
		if r.Similarity != 0 {
			fmt.Fprintf(&b, " | similarity %.2f", r.Similarity)
		}
		b.WriteString("\n")
		if r.Partial {
			b.WriteString("Narrative unavailable: title only, and it could not be verified.\n\n")
			continue
		}
		fmt.Fprintf(&b, "%s\n\n", clip(r.Body, maxBodyChars))
	}
	return b.String()
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "") + "..."
}

// ReadablePrinciples lists principles the caller actually received in full, for
// use-count tracking. A title-only entry does not count as a use.
func ReadablePrinciples(resp Response) []db.Principle {
	var out []db.Principle
	for _, r := range resp.Results {
		if r.Kind == db.RecallKindPrinciple && !r.Partial {
			out = append(out, db.Principle{ID: r.ID})
		}
	}
	return out
}
