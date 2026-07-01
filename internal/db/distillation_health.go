package db

import (
	"time"
)

type DistillationHealth struct {
	LastRunAt               string `json:"last_run_at"`
	LastSuccessAt           string `json:"last_success_at"`
	LastErrorAt             string `json:"last_error_at"`
	LastErrorMessage        string `json:"last_error_message"`
	LastStatus              string `json:"last_status"`
	ConsecutiveFailures     int    `json:"consecutive_failures"`
	TotalSuccesses          int    `json:"total_successes"`
	TotalFailures           int    `json:"total_failures"`
	LastAttemptedEvents     int    `json:"last_attempted_events"`
	LastDistilledPrinciples int    `json:"last_distilled_principles"`
	LastDurationMS          int64  `json:"last_duration_ms"`
	NextScheduledAt         string `json:"next_scheduled_at"`
}

func (d *DB) GetDistillationHealth() (DistillationHealth, error) {
	var h DistillationHealth
	err := d.conn.QueryRow(`
		SELECT
			COALESCE(last_run_at,''),
			COALESCE(last_success_at,''),
			COALESCE(last_error_at,''),
			COALESCE(last_error_message,''),
			COALESCE(last_status,'pending'),
			COALESCE(consecutive_failures,0),
			COALESCE(total_successes,0),
			COALESCE(total_failures,0),
			COALESCE(last_attempted_events,0),
			COALESCE(last_distilled_principles,0),
			COALESCE(last_duration_ms,0),
			COALESCE(next_scheduled_at,'')
		FROM distillation_health
		WHERE id = 'singleton'
	`).Scan(
		&h.LastRunAt,
		&h.LastSuccessAt,
		&h.LastErrorAt,
		&h.LastErrorMessage,
		&h.LastStatus,
		&h.ConsecutiveFailures,
		&h.TotalSuccesses,
		&h.TotalFailures,
		&h.LastAttemptedEvents,
		&h.LastDistilledPrinciples,
		&h.LastDurationMS,
		&h.NextScheduledAt,
	)
	return h, err
}

func (d *DB) RecordDistillationSuccess(runAt time.Time, duration time.Duration, attemptedEvents, distilledPrinciples int, nextScheduledAt time.Time) error {
	_, err := d.conn.Exec(`
		UPDATE distillation_health SET
			last_run_at=?,
			last_success_at=?,
			last_status='success',
			last_error_message='',
			last_error_at='',
			consecutive_failures=0,
			total_successes=total_successes+1,
			last_attempted_events=?,
			last_distilled_principles=?,
			last_duration_ms=?,
			next_scheduled_at=?
		WHERE id='singleton'
	`, runAt.UTC().Format(time.RFC3339), runAt.UTC().Format(time.RFC3339), attemptedEvents, distilledPrinciples, duration.Milliseconds(), nextScheduledAt.UTC().Format(time.RFC3339))
	return err
}

func (d *DB) RecordDistillationFailure(runAt time.Time, duration time.Duration, attemptedEvents int, errMessage string, nextScheduledAt time.Time) error {
	_, err := d.conn.Exec(`
		UPDATE distillation_health SET
			last_run_at=?,
			last_error_at=?,
			last_error_message=?,
			last_status='failed',
			consecutive_failures=consecutive_failures+1,
			total_failures=total_failures+1,
			last_attempted_events=?,
			last_duration_ms=?,
			next_scheduled_at=?
		WHERE id='singleton'
	`, runAt.UTC().Format(time.RFC3339), runAt.UTC().Format(time.RFC3339), errMessage, attemptedEvents, duration.Milliseconds(), nextScheduledAt.UTC().Format(time.RFC3339))
	return err
}
