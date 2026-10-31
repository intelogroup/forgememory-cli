package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/embed"
	"github.com/forge/forge/internal/recall"
)

const (
	embedBatchSize = 32
	embedInterval  = 30 * time.Second
	embedBatchGap  = 200 * time.Millisecond
)

// embedLoop keeps vectors current. It touches the model only when the backlog
// has work, so an idle daemon doesn't keep a model loaded, and it never pulls a
// model itself: that is a download the user should choose.
func embedLoop(database *db.DB, stop <-chan struct{}) {
	client, err := embed.FromEnv()
	if err != nil {
		log.Printf("recall: vector indexing disabled: %v", err)
		return
	}
	if healed, err := database.HealPartialEmbeddings(); err != nil {
		log.Printf("recall: heal partial embeddings: %v", err)
	} else if healed > 0 {
		log.Printf("recall: re-embedding %d principles whose narratives are readable now", healed)
	}

	ticker := time.NewTicker(embedInterval)
	defer ticker.Stop()
	var lastErr string
	for {
		n, err := runEmbedBatches(database, client, stop)
		if n > 0 {
			log.Printf("recall: embedded %d documents", n)
		}
		// Ollama being down is a normal state, so report it once per change.
		if msg := embedErrorMessage(err, client.Model()); msg != lastErr {
			if msg != "" {
				log.Printf("recall: indexing paused, search stays keyword-only for new memories: %s", msg)
			} else {
				log.Printf("recall: indexing resumed")
			}
			lastErr = msg
		}
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

// runEmbedBatches drains the backlog in small batches, pausing between them so
// hooks sharing the single SQLite connection never wait long.
func runEmbedBatches(database *db.DB, e recall.Embedder, stop <-chan struct{}) (int, error) {
	total := 0
	for {
		select {
		case <-stop:
			return total, nil
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), embed.BatchTimeout)
		n, err := recall.IndexBatch(ctx, database, e, embedBatchSize)
		cancel()
		total += n
		if err != nil || n == 0 {
			return total, err
		}
		select {
		case <-stop:
			return total, nil
		case <-time.After(embedBatchGap):
		}
	}
}

func embedErrorMessage(err error, model string) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "not found") {
		return fmt.Sprintf("%v (run: ollama pull %s)", err, model)
	}
	return err.Error()
}
