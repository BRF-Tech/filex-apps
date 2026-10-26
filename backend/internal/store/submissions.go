package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
)

// Submission is one repository at one tag on its way through the pipeline,
// with the names the interface shows next to it.
type Submission struct {
	ID          int64
	PublisherID string
	AppID       *int64
	SubmittedBy int64
	Repo        string
	Ref         string
	// Kind is what was submitted: "app" (an app or a language pack) or
	// "storage" (a storage plugin; migration 0010).
	Kind           string
	Status         string
	Resolution     json.RawMessage
	Name           string
	Version        string
	Note           string
	FailureCode    string
	FailureMessage string
	Attempt        int
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DecidedAt      *time.Time
	DecidedBy      *int64

	Publisher      PublisherRef
	SubmitterLogin string
}

// OpenStatuses are the statuses of a submission still in the pipeline.
var OpenStatuses = []string{"queued", "resolving", "validating", "needs_review"}

const submissionCols = `s.id, s.publisher_id, s.app_id, s.submitted_by, s.repo, s.ref, s.kind, s.status, s.resolution,
	COALESCE(s.name, ''), COALESCE(s.version, ''), COALESCE(s.publisher_note, ''),
	COALESCE(s.failure_code, ''), COALESCE(s.failure_message, ''), s.attempt,
	s.created_at, s.updated_at, s.decided_at, s.decided_by,
	p.id, p.name, p.verified, p.official,
	COALESCE((SELECT i.login FROM identities i WHERE i.account_id = s.submitted_by AND i.provider = 'github'
	          ORDER BY i.last_login_at DESC NULLS LAST LIMIT 1), '')`

const submissionFrom = ` FROM submissions s JOIN publishers p ON p.id = s.publisher_id `

func scanSubmission(row scanner) (Submission, error) {
	var s Submission
	err := row.Scan(&s.ID, &s.PublisherID, &s.AppID, &s.SubmittedBy, &s.Repo, &s.Ref, &s.Kind, &s.Status, &s.Resolution,
		&s.Name, &s.Version, &s.Note, &s.FailureCode, &s.FailureMessage, &s.Attempt,
		&s.CreatedAt, &s.UpdatedAt, &s.DecidedAt, &s.DecidedBy,
		&s.Publisher.ID, &s.Publisher.Name, &s.Publisher.Verified, &s.Publisher.Official, &s.SubmitterLogin)
	return s, err
}

// OpenSubmissionError is ErrConflict with the submission already in the
// pipeline for the same repository and tag.
type OpenSubmissionError struct {
	ID int64
}

func (e *OpenSubmissionError) Error() string {
	return "a submission for this repository and tag is open"
}

// Unwrap makes errors.Is(err, ErrConflict) true.
func (e *OpenSubmissionError) Unwrap() error { return ErrConflict }

// InsertSubmission queues a new submission. *OpenSubmissionError when the
// repository at this tag is already in the pipeline.
func InsertSubmission(ctx context.Context, q Q, publisherID string, by int64, repo, ref, note string) (int64, error) {
	return InsertSubmissionKind(ctx, q, publisherID, by, repo, ref, note, "app")
}

// InsertSubmissionKind is InsertSubmission for a kind: "app" (an app or a
// language pack) or "storage" (a storage plugin).
func InsertSubmissionKind(ctx context.Context, q Q, publisherID string, by int64, repo, ref, note, kind string) (int64, error) {
	if kind == "" {
		kind = "app"
	}
	var id int64
	err := q.QueryRow(ctx, `
		INSERT INTO submissions (publisher_id, submitted_by, repo, ref, publisher_note, kind)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6) RETURNING id`, publisherID, by, repo, ref, note, kind).Scan(&id)
	if err != nil && isUnique(err) {
		open, ferr := FindOpenSubmission(ctx, q, repo, ref)
		if ferr != nil {
			return 0, err
		}
		return 0, &OpenSubmissionError{ID: open}
	}
	return id, err
}

// FindOpenSubmission returns the id of the open submission for the
// repository at the tag (ErrNotFound when none).
func FindOpenSubmission(ctx context.Context, q Q, repo, ref string) (int64, error) {
	var id int64
	err := q.QueryRow(ctx, `
		SELECT id FROM submissions WHERE lower(repo) = lower($1) AND ref = $2 AND status = ANY($3)
		ORDER BY id DESC LIMIT 1`, repo, ref, OpenStatuses).Scan(&id)
	return id, notFound(err)
}

// GetSubmission by id.
func GetSubmission(ctx context.Context, q Q, id int64) (Submission, error) {
	s, err := scanSubmission(q.QueryRow(ctx, `SELECT `+submissionCols+submissionFrom+`WHERE s.id = $1`, id))
	return s, notFound(err)
}

// LockSubmission reads a submission FOR UPDATE (inside a transaction).
func LockSubmission(ctx context.Context, q Q, id int64) (Submission, error) {
	s, err := scanSubmission(q.QueryRow(ctx, `SELECT `+submissionCols+submissionFrom+`WHERE s.id = $1 FOR UPDATE OF s`, id))
	return s, notFound(err)
}

// SubmissionFilter narrows a listing.
type SubmissionFilter struct {
	Status    string
	Publisher string
	// MemberOf, when set, keeps the submissions of publishers this account
	// is a member of (a publisher's view); nil means all (a reviewer's).
	MemberOf *int64
	// OldestFirst orders by creation ascending (the review queue).
	OldestFirst bool
}

// ListSubmissions returns one page and the total.
func ListSubmissions(ctx context.Context, q Q, f SubmissionFilter, page Page) ([]Submission, int, error) {
	where := `WHERE ($1 = '' OR s.status = $1) AND ($2 = '' OR s.publisher_id = $2)
		AND ($3::bigint IS NULL OR s.publisher_id IN (SELECT publisher_id FROM publisher_members WHERE account_id = $3))`
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*)`+submissionFrom+where, f.Status, f.Publisher, f.MemberOf).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := ` ORDER BY s.created_at DESC, s.id DESC`
	if f.OldestFirst {
		order = ` ORDER BY s.created_at, s.id`
	}
	rows, err := q.Query(ctx, `SELECT `+submissionCols+submissionFrom+where+order+` LIMIT $4 OFFSET $5`,
		f.Status, f.Publisher, f.MemberOf, page.Limit(), page.Offset())
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Submission{}
	for rows.Next() {
		s, err := scanSubmission(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, rows.Err()
}

// Transition moves a submission at the given attempt from one of the
// statuses in from to status to. It reports false when the submission was
// no longer in one of those (another worker or a person got there first).
func Transition(ctx context.Context, q Q, id int64, attempt int, from []string, to string) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE submissions SET status = $4, updated_at = now()
		WHERE id = $1 AND attempt = $2 AND status = ANY($3)`, id, attempt, from, to)
	return tag.RowsAffected() == 1, err
}

// SetResolved stores the resolver's result and moves the submission to
// "validating".
func SetResolved(ctx context.Context, q Q, id int64, attempt int, res *domain.Resolution) (bool, error) {
	b, err := json.Marshal(res)
	if err != nil {
		return false, err
	}
	tag, err := q.Exec(ctx, `
		UPDATE submissions SET status = 'validating', resolution = $3, name = NULLIF($4, ''), version = NULLIF($5, ''),
		       updated_at = now()
		WHERE id = $1 AND attempt = $2 AND status = 'resolving'`, id, attempt, b, res.Name, res.Version)
	return tag.RowsAffected() == 1, err
}

// Fail marks the attempt failed with the first failing check's reason.
func Fail(ctx context.Context, q Q, id int64, attempt int, from []string, code, message string) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE submissions SET status = 'failed', failure_code = $4, failure_message = $5, updated_at = now()
		WHERE id = $1 AND attempt = $2 AND status = ANY($3)`, id, attempt, from, code, message)
	return tag.RowsAffected() == 1, err
}

// Withdraw ends a submission that is not decided yet.
func Withdraw(ctx context.Context, q Q, id int64) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE submissions SET status = 'withdrawn', updated_at = now()
		WHERE id = $1 AND status = ANY($2)`, id, OpenStatuses)
	return tag.RowsAffected() == 1, err
}

// Retry starts a new attempt of a failed submission. ErrConflict (an
// *OpenSubmissionError) when the repository at this tag is open again.
func Retry(ctx context.Context, q Q, id int64) (int, error) {
	var attempt int
	err := q.QueryRow(ctx, `
		UPDATE submissions SET status = 'queued', attempt = attempt + 1, failure_code = NULL, failure_message = NULL,
		       resolution = NULL, updated_at = now()
		WHERE id = $1 AND status = 'failed' RETURNING attempt`, id).Scan(&attempt)
	if err != nil && isUnique(err) {
		s, gerr := GetSubmission(ctx, q, id)
		if gerr == nil {
			if open, ferr := FindOpenSubmission(ctx, q, s.Repo, s.Ref); ferr == nil {
				return 0, &OpenSubmissionError{ID: open}
			}
		}
		return 0, ErrConflict
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return attempt, err
}

// Decide records a reviewer's decision (approved / rejected).
func Decide(ctx context.Context, q Q, id int64, status string, by int64, appID *int64) error {
	_, err := q.Exec(ctx, `
		UPDATE submissions SET status = $2, decided_at = now(), decided_by = $3, app_id = COALESCE($4, app_id),
		       updated_at = now()
		WHERE id = $1`, id, status, by, appID)
	return err
}

// InsertChecks writes check rows for an attempt.
func InsertChecks(ctx context.Context, q Q, submissionID int64, attempt int, checks []domain.CheckResult) error {
	for _, c := range checks {
		var data any
		if len(c.Data) > 0 && string(c.Data) != "null" {
			data = c.Data
		}
		started := c.StartedAt
		if started.IsZero() {
			started = time.Now()
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO submission_checks (submission_id, attempt, kind, validator, status, code, message, data, started_at, ended_at)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), $8, $9, $10)`,
			submissionID, attempt, string(c.Kind), c.Validator, string(c.Status), c.Code, c.Message, data, started, c.EndedAt); err != nil {
			return err
		}
	}
	return nil
}

// ChecksOf lists an attempt's checks in the order they were written.
func ChecksOf(ctx context.Context, q Q, submissionID int64, attempt int) ([]domain.CheckResult, error) {
	rows, err := q.Query(ctx, `
		SELECT kind, validator, status, COALESCE(code, ''), COALESCE(message, ''), data, started_at, ended_at
		FROM submission_checks WHERE submission_id = $1 AND attempt = $2 ORDER BY id`, submissionID, attempt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.CheckResult{}
	for rows.Next() {
		var c domain.CheckResult
		var kind, status string
		var data []byte
		if err := rows.Scan(&kind, &c.Validator, &status, &c.Code, &c.Message, &data, &c.StartedAt, &c.EndedAt); err != nil {
			return nil, err
		}
		c.Kind, c.Status = domain.CheckKind(kind), domain.CheckStatus(status)
		if len(data) > 0 {
			c.Data = json.RawMessage(data)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteChecks removes an attempt's checks of one kind (a recheck replaces
// the dry runs).
func DeleteChecks(ctx context.Context, q Q, submissionID int64, attempt int, kind domain.CheckKind) error {
	_, err := q.Exec(ctx, `DELETE FROM submission_checks WHERE submission_id = $1 AND attempt = $2 AND kind = $3`,
		submissionID, attempt, string(kind))
	return err
}

// Review is a reviewer's decision or comment.
type Review struct {
	ID           int64
	SubmissionID int64
	ReviewerID   int64
	Reviewer     string
	Decision     string // approve | reject | comment
	Note         string
	Security     bool
	CreatedAt    time.Time
}

// InsertReview records a review row.
func InsertReview(ctx context.Context, q Q, r Review) (Review, error) {
	err := q.QueryRow(ctx, `
		INSERT INTO reviews (submission_id, reviewer_id, decision, note, security)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5) RETURNING id, created_at`,
		r.SubmissionID, r.ReviewerID, r.Decision, r.Note, r.Security).Scan(&r.ID, &r.CreatedAt)
	if err != nil {
		return Review{}, err
	}
	err = q.QueryRow(ctx, `SELECT display_name FROM accounts WHERE id = $1`, r.ReviewerID).Scan(&r.Reviewer)
	return r, err
}

// ReviewsOf lists a submission's reviews, oldest first.
func ReviewsOf(ctx context.Context, q Q, submissionID int64) ([]Review, error) {
	rows, err := q.Query(ctx, `
		SELECT r.id, r.submission_id, r.reviewer_id, a.display_name, r.decision, COALESCE(r.note, ''), r.security, r.created_at
		FROM reviews r JOIN accounts a ON a.id = r.reviewer_id
		WHERE r.submission_id = $1 ORDER BY r.id`, submissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Review{}
	for rows.Next() {
		var r Review
		if err := rows.Scan(&r.ID, &r.SubmissionID, &r.ReviewerID, &r.Reviewer, &r.Decision, &r.Note, &r.Security, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
