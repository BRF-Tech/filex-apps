package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Job kinds (the jobs.kind CHECK).
const (
	JobResolve        = "resolve"
	JobDryRun         = "dry_run"
	JobPublishIndex   = "publish_index"
	JobRefreshIndex   = "refresh_index"
	JobExpireSessions = "expire_sessions"
)

// Job is one claimed unit of work.
type Job struct {
	ID          int64
	Kind        string
	Payload     json.RawMessage
	Attempts    int // including this run
	MaxAttempts int
}

// MaxAttempts is how many times a job of the kind is tried before it fails
// for good. A submission's jobs (resolve, dry_run) wait out an outage of
// GitHub or of a validator: with the pipeline's backoff (10 s, times three,
// at most 15 minutes) their ninth try comes about 65 minutes after the first
// (GitHub's anonymous downloads failed for hours on 2026-11-08). Every other
// kind keeps the table's default.
func MaxAttempts(kind string) int {
	switch kind {
	case JobResolve, JobDryRun:
		return 9
	}
	return 5
}

// Enqueue adds a job; runAfter zero means now. The job may be tried
// MaxAttempts(kind) times.
func Enqueue(ctx context.Context, q Q, kind string, payload any, runAfter time.Time) (int64, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	var id int64
	err = q.QueryRow(ctx, `
		INSERT INTO jobs (kind, payload, run_after, max_attempts) VALUES ($1, $2, COALESCE($3, now()), $4) RETURNING id`,
		kind, b, nullTime(runAfter), MaxAttempts(kind)).Scan(&id)
	return id, err
}

// Claim takes the oldest ready job of the kinds (FOR UPDATE SKIP LOCKED),
// marks it running under a lease and counts the attempt. nil when there is
// nothing to do.
func Claim(ctx context.Context, q Q, kinds []string, worker string, lease time.Duration) (*Job, error) {
	var j Job
	err := q.QueryRow(ctx, `
		UPDATE jobs SET status = 'running', locked_by = $2, locked_until = now() + $3 * interval '1 millisecond',
		       attempts = attempts + 1, updated_at = now()
		WHERE id = (
			SELECT id FROM jobs
			WHERE status = 'queued' AND run_after <= now() AND kind = ANY($1)
			ORDER BY run_after, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1)
		RETURNING id, kind, payload, attempts, max_attempts`, kinds, worker, lease.Milliseconds()).
		Scan(&j.ID, &j.Kind, &j.Payload, &j.Attempts, &j.MaxAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// CompleteJob marks a job done.
func CompleteJob(ctx context.Context, q Q, id int64) error {
	_, err := q.Exec(ctx, `
		UPDATE jobs SET status = 'done', locked_by = NULL, locked_until = NULL, updated_at = now()
		WHERE id = $1 AND status = 'running'`, id)
	return err
}

// RetryJob puts a job back in the queue after a delay, keeping the error.
func RetryJob(ctx context.Context, q Q, id int64, msg string, after time.Duration) error {
	_, err := q.Exec(ctx, `
		UPDATE jobs SET status = 'queued', locked_by = NULL, locked_until = NULL, last_error = $2,
		       run_after = now() + $3 * interval '1 millisecond', updated_at = now()
		WHERE id = $1 AND status = 'running'`, id, truncate(msg, 2000), after.Milliseconds())
	return err
}

// FailJob gives up on a job.
func FailJob(ctx context.Context, q Q, id int64, msg string) error {
	_, err := q.Exec(ctx, `
		UPDATE jobs SET status = 'failed', locked_by = NULL, locked_until = NULL, last_error = $2, updated_at = now()
		WHERE id = $1 AND status = 'running'`, id, truncate(msg, 2000))
	return err
}

// RequeueExpired returns running jobs whose lease ran out (a worker died)
// to the queue, or fails them when they have used their attempts.
func RequeueExpired(ctx context.Context, q Q) (int64, error) {
	tag, err := q.Exec(ctx, `
		UPDATE jobs SET
			status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'queued' END,
			last_error = COALESCE(last_error, '') || CASE WHEN last_error IS NULL THEN '' ELSE '; ' END || 'lease expired',
			locked_by = NULL, locked_until = NULL, updated_at = now()
		WHERE status = 'running' AND locked_until < now()`)
	return tag.RowsAffected(), err
}

// Retrying is a submission's job that failed a try and waits for the next
// one: what the operator import prints while it waits.
type Retrying struct {
	Kind      string
	Attempts  int // tries made
	Max       int
	LastError string
	Next      time.Time
}

// SubmissionRetrying is the newest queued job of the submission that has
// failed a try; nil when there is none.
func SubmissionRetrying(ctx context.Context, q Q, submissionID int64) (*Retrying, error) {
	var r Retrying
	err := q.QueryRow(ctx, `
		SELECT kind, attempts, max_attempts, last_error, run_after FROM jobs
		WHERE status = 'queued' AND attempts > 0 AND last_error IS NOT NULL AND payload ->> 'submission_id' = $1
		ORDER BY id DESC LIMIT 1`, strconv.FormatInt(submissionID, 10)).Scan(&r.Kind, &r.Attempts, &r.Max, &r.LastError, &r.Next)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// CancelSubmissionJobs cancels the queued jobs of a submission.
func CancelSubmissionJobs(ctx context.Context, q Q, submissionID int64) (int64, error) {
	tag, err := q.Exec(ctx, `
		UPDATE jobs SET status = 'cancelled', updated_at = now()
		WHERE status = 'queued' AND payload ->> 'submission_id' = $1`, strconv.FormatInt(submissionID, 10))
	return tag.RowsAffected(), err
}

// JobCounts counts jobs per status (tests and the readiness answer).
func JobCounts(ctx context.Context, q Q) (map[string]int, error) {
	rows, err := q.Query(ctx, `SELECT status, count(*) FROM jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
