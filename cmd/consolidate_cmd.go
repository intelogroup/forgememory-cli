package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
	"github.com/google/uuid"
)

// ConsolidationResult describes one consolidation decision.
type ConsolidationResult struct {
	Operation   string   `json:"operation"` // ADD | UPDATE | DELETE | NONE
	OriginalID  string   `json:"original_id,omitempty"`
	NewID       string   `json:"new_id,omitempty"`
	Title       string   `json:"title,omitempty"`
	Narrative   string   `json:"narrative,omitempty"`
	Type        string   `json:"type,omitempty"`
	ImpactScore float64  `json:"impact_score,omitempty"`
	ProjectID   string   `json:"project_id,omitempty"`
	SourceEvent string   `json:"source_event,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	Scope       string   `json:"scope,omitempty"` // project | global
	Evidence    []string `json:"evidence,omitempty"`
}

// consolidationBatch groups the decisions produced by one LLM call so the
// apply layer can commit or roll back that batch as a unit.
type consolidationBatch struct {
	id      string
	results []ConsolidationResult
}

// consolidationCtx holds the in-memory state during a consolidation run.
type consolidationCtx struct {
	database      *db.DB
	principles    []db.Principle
	batches       []*consolidationBatch
	dryRun        bool
	verbose       bool
	model         string
	rejected      int
	failedBatches int
}

// allResults flattens every batch for the dry-run report.
func (c *consolidationCtx) allResults() []ConsolidationResult {
	var out []ConsolidationResult
	for _, b := range c.batches {
		out = append(out, b.results...)
	}
	return out
}

func runConsolidate(args []string) {
	fs := flag.NewFlagSet("consolidate", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "Apply the consolidation decisions (default is a dry run)")
	dryRun := fs.Bool("dry-run", false, "Print decisions without applying them (default)")
	verbose := fs.Bool("verbose", false, "Print detailed progress")
	project := fs.String("project", "", "Only consider principles from this project (default: all projects)")
	limit := fs.Int("limit", 0, "Maximum principles to consider, highest impact first (0 = all)")
	help := fs.Bool("help", false, "Show this help message")

	// A parse error (unknown flag, -h) must not fall through to the apply path.
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if *help {
		fmt.Println(helpText())
		return
	}

	if *apply && *dryRun {
		fmt.Fprintln(os.Stderr, "Error: --apply and --dry-run are mutually exclusive")
		os.Exit(2)
	}
	if *limit < 0 {
		fmt.Fprintln(os.Stderr, "Error: --limit must be >= 0")
		os.Exit(2)
	}

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	cfg := distill.LoadConfig()
	if cfg.Provider == "" {
		fmt.Fprintln(os.Stderr, "Error: no inference provider configured (set FORGE_PROVIDER/FORGE_API_KEY)")
		os.Exit(1)
	}

	principles, err := loadPrinciplesForConsolidation(database, *project, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading principles: %v\n", err)
		os.Exit(1)
	}

	if *verbose {
		scope := "all projects"
		if *project != "" {
			scope = "project " + *project
		}
		fmt.Printf("Loaded %d principles from DB (%s)\n", len(principles), scope)
	}

	ctx := &consolidationCtx{
		database:   database,
		principles: principles,
		dryRun:     !*apply,
		verbose:    *verbose,
		model:      cfg.Model,
	}

	// Process in batches to avoid overly long LLM prompts.
	batchSize := 12
	totalOps := map[string]int{}
	runID := time.Now().UTC().Format("20060102T150405")
	for i := 0; i < len(ctx.principles); i += batchSize {
		end := i + batchSize
		if end > len(ctx.principles) {
			end = len(ctx.principles)
		}
		batch := ctx.principles[i:end]

		if ctx.verbose {
			fmt.Printf("Processing batch %d-%d of %d\n", i+1, end, len(ctx.principles))
		}

		results, err := ctx.ConsolidateBatch(batch)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Batch %d-%d failed: %v\n", i+1, end, err)
			ctx.failedBatches++
			continue
		}

		valid, rejected, validateErr := ctx.validateResults(batch, results)
		if validateErr != nil {
			fmt.Fprintf(os.Stderr, "Batch %d-%d: %v\n", i+1, end, validateErr)
			ctx.failedBatches++
			continue
		}
		ctx.rejected += rejected
		if rejected > 0 {
			fmt.Fprintf(os.Stderr, "Batch %d-%d: rejected %d invalid decision(s)\n", i+1, end, rejected)
		}

		ctx.batches = append(ctx.batches, &consolidationBatch{
			id:      fmt.Sprintf("consolidate-%s-b%d", runID, len(ctx.batches)+1),
			results: valid,
		})
		for _, r := range valid {
			totalOps[r.Operation]++
		}
	}

	if ctx.dryRun {
		fmt.Println("DRY RUN — no changes applied (re-run with --apply to write)")
		printJsonResults(ctx.allResults())
	} else {
		if err := ctx.applyResults(); err != nil {
			fmt.Fprintf(os.Stderr, "Error applying consolidation: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Consolidation complete. Operations: %v\n", totalOps)
		if ctx.rejected > 0 {
			fmt.Fprintf(os.Stderr, "%d decision(s) rejected as invalid; nothing was written for them\n", ctx.rejected)
		}
	}

	// A batch that could not be processed makes the run incomplete in either
	// mode; report it and fail rather than printing a clean summary.
	if ctx.failedBatches > 0 {
		fmt.Fprintf(os.Stderr, "%d batch(es) failed and were not applied\n", ctx.failedBatches)
		os.Exit(1)
	}

	if ctx.verbose {
		fmt.Printf("Total results: %d\n", len(ctx.allResults()))
	}
}

func helpText() string {
	return `Usage: forge consolidate [flags]

Consolidates distilled principles using Mem0-style four-op logic (ADD/UPDATE/DELETE/NONE)
over batches of active principles across all projects.

This command is a dry run by default: decisions are printed and nothing is written.

Options:
  --apply      Apply the decisions to the database
  --dry-run    Explicit dry run (default when --apply is absent)
  --project ID Only consider principles from this project
  --limit N    Consider at most N principles, highest impact first (0 = all)
  --verbose    Print detailed progress information
  --help       Show this help message

Examples:
  forge consolidate
  forge consolidate --limit 24 --verbose
  forge consolidate --project myapp --apply --verbose`
}

// loadPrinciplesForConsolidation loads active principles (optionally scoped to
// one project), ranks them by impact and recency, and applies the --limit cap
// so a dry run over a large store stays cheap.
func loadPrinciplesForConsolidation(database *db.DB, projectID string, limit int) ([]db.Principle, error) {
	principles, err := database.RecentPrinciplesByProject(projectID, 10000)
	if err != nil {
		return nil, err
	}
	sortPrinciples(principles)
	if limit > 0 && len(principles) > limit {
		principles = principles[:limit]
	}
	return principles, nil
}

func sortPrinciples(principles []db.Principle) {
	sort.Slice(principles, func(i, j int) bool {
		if principles[i].ImpactScore != principles[j].ImpactScore {
			return principles[i].ImpactScore > principles[j].ImpactScore
		}
		return principles[i].TS > principles[j].TS
	})
}

func printJsonResults(results []ConsolidationResult) {
	data, _ := json.MarshalIndent(results, "", "  ")
	fmt.Println(string(data))
}

// validateResults treats the model's response as untrusted input: every
// decision must target a principle from the batch that produced it, carry the
// evidence and reason the prompt requires, and stay inside the ranges the
// store assumes. Invalid decisions are dropped and counted, never applied.
//
// Evidence must be either an ID from this batch or a stored event/session ID;
// fabricated IDs are rejected. This proves the citation points at something
// real, not that it supports the claim — external grounding arrives with
// source_episodes in the scope/evidence model.
func (c *consolidationCtx) validateResults(batch []db.Principle, results []ConsolidationResult) (valid []ConsolidationResult, rejected int, err error) {
	inBatch := make(map[string]bool, len(batch))
	for _, p := range batch {
		inBatch[p.ID] = true
	}
	resolved := make(map[string]bool)

	for _, res := range results {
		res.Operation = strings.ToUpper(strings.TrimSpace(res.Operation))
		res.Title = strings.TrimSpace(res.Title)
		res.Narrative = strings.TrimSpace(res.Narrative)
		res.Type = strings.TrimSpace(res.Type)
		res.Reason = strings.TrimSpace(res.Reason)
		res.Scope = strings.ToLower(strings.TrimSpace(res.Scope))
		if res.Scope == "" {
			// The model usually restates the current scope; when it omits it,
			// keep the lesson project-local rather than silently globalizing it.
			res.Scope = "project"
		}
		scopeOK := res.Scope == "project" || res.Scope == "global"
		res.ProjectID = strings.TrimSpace(res.ProjectID)
		if res.ImpactScore < 0 {
			res.ImpactScore = 0
		} else if res.ImpactScore > 1 {
			res.ImpactScore = 1
		}

		ok := true
		switch res.Operation {
		case "NONE":
			valid = append(valid, res)
			continue
		case "UPDATE":
			// Anchor the edit to the row the model says it is updating. A
			// model that ignores "reuse original_id as new_id" and invents a
			// new_id must not make the decision unappliable (or retarget it).
			target := res.OriginalID
			if target == "" {
				target = res.NewID
			}
			ok = inBatch[target] && res.Title != "" && res.Narrative != ""
			if ok {
				res.OriginalID = target
				res.NewID = target
			}
		case "DELETE":
			ok = inBatch[res.OriginalID]
		case "ADD":
			ok = res.NewID != "" && res.Title != "" && res.Narrative != "" &&
				res.Type != "" && res.ProjectID != ""
		default:
			ok = false
		}
		if !ok || !scopeOK || res.Reason == "" {
			rejected++
			continue
		}

		evidence := res.Evidence[:0]
		evidenceValid := true
		for _, e := range res.Evidence {
			e = strings.TrimSpace(e)
			if e == "" {
				continue
			}
			if inBatch[e] {
				evidence = append(evidence, e)
				continue
			}
			found, cached := resolved[e]
			if !cached {
				found, err = c.database.EvidenceExists(e)
				if err != nil {
					return nil, 0, fmt.Errorf("resolve evidence %q: %w", e, err)
				}
				resolved[e] = found
			}
			if !found {
				evidenceValid = false
				break
			}
			evidence = append(evidence, e)
		}
		res.Evidence = evidence
		if !evidenceValid || len(evidence) == 0 {
			rejected++
			continue
		}
		valid = append(valid, res)
	}
	return valid, rejected, nil
}

// ---- Consolidation core ----

// ConsolidateBatch sends a batch of principles to the LLM for consolidation.
func (c *consolidationCtx) ConsolidateBatch(batch []db.Principle) ([]ConsolidationResult, error) {
	if len(batch) == 0 {
		return nil, nil
	}

	batchJSON, err := json.MarshalIndent(batch, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal batch: %w", err)
	}

	prompt := c.buildPrompt(batch, string(batchJSON))

	resp, err := c.callLLM(prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM generate: %w", err)
	}

	return parseResponse(resp)
}

// callLLM invokes the configured inference provider via the distiller stack.
func (c *consolidationCtx) callLLM(prompt string) (string, error) {
	cfg := distill.LoadConfig()
	d := distill.New(c.database, cfg)
	d.SetSessionContext("You are consolidating Forge memory principles across sessions.")
	return d.CallLLM(prompt)
}

// buildPrompt constructs the Mem0-adapted consolidation prompt for a batch.
func (c *consolidationCtx) buildPrompt(batch []db.Principle, batchJSON string) string {
	var candidateLines []string
	for _, p := range batch {
		narr := p.Narrative
		if len(narr) > 140 {
			narr = narr[:140] + "..."
		}
		candidateLines = append(candidateLines, fmt.Sprintf("  - %s (%s, impact=%.2f, project=%s): %s",
			p.Title, p.Type, p.ImpactScore, p.ProjectID, narr))
	}

	return `You are a smart memory manager controlling Forge's memory of distilled principles across agent sessions.

You can perform four operations:
  (1) ADD — new information not present in existing memory, generate a new ID
  (2) UPDATE — facts convey new/different information about an existing element; keep richer text, reuse original ID
  (3) DELETE — contradiction or noise/duplicate — BUT never hard delete; instead EXPIRE (set valid_until, mark superseded_by)
  (4) NONE — already present or irrelevant, make no change

CRITICAL RULES:
1. Never DELETE a principle outright on contradiction — instead return operation "DELETE" with a note that it should be EXPIRED (valid_until set, superseded_by pointing to replacement).
2. Cross-project consolidation is allowed only when evidence from both projects is cited in the evidence array.
3. Every non-NONE op MUST include: reason (one-line), scope ("project" or "global"), and evidence — an array of IDs you can actually see: a principle id from this batch, or a stored event/session id. Never invent IDs (no placeholders); if none applies, return NONE.
4. Keep IDs stable for UPDATE — reuse original_id as new_id, do not generate new IDs for UPDATE.
5. ADD must always have a new_id (UUID-style string) and a non-empty project_id and type.
6. For DELETE ops, set new_id = original_id so the caller knows which row to expire.

Existing memory (batch of candidates already sorted by impact_score DESC, ts DESC):
` + strings.Join(candidateLines, "\n") + `

Batch JSON (full fields):
` + batchJSON + `

Return JSON only — an array of objects with fields:
operation, original_id, new_id, title, narrative, type, impact_score, project_id,
source_event, reason, scope, evidence (array of event IDs).

Example: [{"operation":"UPDATE","original_id":"<id from the batch>","new_id":"<same id>","title":"use transactions","narrative":"Always wrap multi-row writes in a transaction...","type":"pattern","impact_score":0.85,"project_id":"<project from the batch>","source_event":"<source_event from the batch>","reason":"Expanded with concrete example","scope":"project","evidence":["<source_event copied from the batch>"]}]
`
}

// parseResponse extracts the JSON array from the LLM response.
func parseResponse(resp string) ([]ConsolidationResult, error) {
	resp = strings.TrimSpace(resp)
	if strings.HasPrefix(resp, "```json") {
		resp = strings.TrimPrefix(resp, "```json")
		resp = strings.TrimSuffix(resp, "```")
		resp = strings.TrimSpace(resp)
	}

	var results []ConsolidationResult
	if err := json.Unmarshal([]byte(resp), &results); err != nil {
		// Try to extract the array substring
		start := strings.Index(resp, "[")
		end := strings.LastIndex(resp, "]")
		if start == -1 || end == -1 || end < start {
			return nil, fmt.Errorf("no JSON array found in response: %q", truncateStr(resp, 200))
		}
		if err := json.Unmarshal([]byte(resp[start:end+1]), &results); err != nil {
			return nil, fmt.Errorf("unmarshal response: %w", err)
		}
	}

	return results, nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---- Apply layer ----

// applyResults commits each batch in its own transaction. A failing decision
// rolls back the whole batch, so a run never leaves memory half-consolidated
// and a failed audit row never describes a mutation that did not happen.
func (c *consolidationCtx) applyResults() error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, b := range c.batches {
		if len(b.results) == 0 {
			continue
		}
		batch := b
		err := c.database.WithTx(func(tx *sql.Tx) error {
			for _, res := range batch.results {
				switch res.Operation {
				case "UPDATE":
					if err := c.applyUpdate(tx, res, now, batch.id); err != nil {
						return fmt.Errorf("UPDATE %s: %w", res.NewID, err)
					}
				case "DELETE":
					// Per rules: never hard-delete — expire via valid_until / superseded_by
					if err := c.applyExpire(tx, res, now, batch.id, batch.results); err != nil {
						return fmt.Errorf("DELETE %s: %w", res.OriginalID, err)
					}
				case "ADD":
					if err := c.applyAdd(tx, res, now, batch.id); err != nil {
						return fmt.Errorf("ADD: %w", err)
					}
				case "NONE":
					// No action needed.
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("batch %s: %w", batch.id, err)
		}
	}
	return nil
}

// applyUpdate issues an UPDATE to the principle row and writes an audit row.
func (c *consolidationCtx) applyUpdate(tx *sql.Tx, res ConsolidationResult, now, batchID string) error {
	// Validation anchors UPDATE to original_id; fall back here as well so the
	// apply layer is safe when called directly.
	target := res.NewID
	if target == "" {
		target = res.OriginalID
	}
	before, err := c.database.GetPrincipleByIDTx(tx, target)
	if err != nil {
		return fmt.Errorf("fetching principle %s: %w", target, err)
	}
	if before == nil {
		return fmt.Errorf("principle %s not found", target)
	}

	implHint := before.ImplHint

	auditBefore, _ := json.Marshal(map[string]any{
		"title":        before.Title,
		"narrative":    before.Narrative,
		"impact_score": before.ImpactScore,
	})
	auditAfter, _ := json.Marshal(map[string]any{
		"title":        res.Title,
		"narrative":    res.Narrative,
		"impact_score": res.ImpactScore,
	})

	// The DB layer re-signs and re-encrypts the new text; writing title and
	// narrative directly here would leave a stale HMAC and the row would be
	// dropped by every read path.
	if err := c.database.UpdatePrincipleContentTx(tx, target, res.Title, res.Narrative, res.ImpactScore, implHint, res.Scope); err != nil {
		return fmt.Errorf("update principle: %w", err)
	}

	if err := c.database.InsertMemoryOpTx(
		tx, newOpID(), now, "UPDATE", target,
		string(auditBefore), string(auditAfter),
		res.Reason, c.model, batchID,
	); err != nil {
		return fmt.Errorf("write memory_ops audit: %w", err)
	}

	return nil
}

// applyExpire marks a principle expired (valid_until) rather than deleting it.
func (c *consolidationCtx) applyExpire(tx *sql.Tx, res ConsolidationResult, now, batchID string, batchResults []ConsolidationResult) error {
	res.NewID = res.OriginalID

	// If this is a consolidation where a NEW principle replaces an old one,
	// find the new ID among this batch's results with a matching title.
	for _, r := range batchResults {
		if r.Operation == "ADD" && r.Title == res.Title {
			res.NewID = r.NewID
			break
		}
	}

	auditBefore, _ := json.Marshal(map[string]any{
		"operation":  res.Operation,
		"expired_at": now,
	})

	// Expire the principle row (temporal audit trail: keep row, mark revoked).
	// ExpirePrinciple also unlinks any conflict peer pointing at this row.
	if err := c.database.ExpirePrincipleTx(tx, res.OriginalID, now, res.NewID); err != nil {
		return fmt.Errorf("expire principle: %w", err)
	}

	if err := c.database.InsertMemoryOpTx(
		tx, newOpID(), now, "EXPIRE", res.OriginalID,
		string(auditBefore), "",
		res.Reason, c.model, batchID,
	); err != nil {
		return fmt.Errorf("write memory_ops audit: %w", err)
	}

	return nil
}

// applyAdd inserts a new principle, or revives an expired one that owns the
// same fingerprint. A fingerprint collision with an active row (or a manual
// revocation) is recorded as SKIP rather than silently dropped.
func (c *consolidationCtx) applyAdd(tx *sql.Tx, res ConsolidationResult, now, batchID string) error {
	p := &db.Principle{
		ID:          res.NewID,
		TS:          now,
		Type:        res.Type,
		Title:       res.Title,
		Narrative:   res.Narrative,
		ImpactScore: res.ImpactScore,
		ProjectID:   res.ProjectID,
		SourceEvent: res.SourceEvent,
		Scope:       res.Scope,
		Status:      "active",
		LastUsedTS:  now,
		UseCount:    0,
	}

	inserted, err := c.database.InsertPrincipleTx(tx, p)
	if err != nil {
		return fmt.Errorf("insert principle: %w", err)
	}

	if !inserted {
		revived, existingID, revErr := c.database.RevivePrincipleIfExpiredTx(tx, p)
		if revErr != nil {
			return fmt.Errorf("revive principle: %w", revErr)
		}
		if !revived {
			if err := c.database.InsertMemoryOpTx(
				tx, newOpID(), now, "SKIP", existingID, "", "",
				"duplicate fingerprint; not adding: "+res.Reason, c.model, batchID,
			); err != nil {
				return fmt.Errorf("write memory_ops audit: %w", err)
			}
			if c.verbose {
				fmt.Printf("SKIP: %s (same fingerprint already active or manually revoked)\n", truncateStr(res.Title, 40))
			}
			return nil
		}

		after, _ := json.Marshal(map[string]any{
			"title":        res.Title,
			"narrative":    res.Narrative,
			"impact_score": res.ImpactScore,
		})
		if err := c.database.InsertMemoryOpTx(
			tx, newOpID(), now, "REVIVE", existingID, "", string(after),
			res.Reason, c.model, batchID,
		); err != nil {
			return fmt.Errorf("write memory_ops audit: %w", err)
		}
		if c.verbose {
			fmt.Printf("REVIVE: %s [%s] — %s\n", truncateStr(res.Title, 40), res.Scope, res.Reason)
		}
		return nil
	}

	// Audit the ADD
	after, _ := json.Marshal(map[string]any{
		"title":        res.Title,
		"narrative":    res.Narrative,
		"impact_score": res.ImpactScore,
	})
	if err := c.database.InsertMemoryOpTx(
		tx, newOpID(), now, "ADD", p.ID,
		"", string(after),
		res.Reason, c.model, batchID,
	); err != nil {
		return fmt.Errorf("write memory_ops audit: %w", err)
	}

	if c.verbose {
		fmt.Printf("ADD: %s [%s] — %s\n", truncateStr(res.Title, 40), res.Scope, res.Reason)
	}
	return nil
}

// newOpID returns a collision-resistant memory_ops ID with a single prefix.
func newOpID() string {
	return "op-" + uuid.New().String()
}
