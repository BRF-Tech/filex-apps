package store

import (
	"context"
	"time"
)

// Publisher is who stands behind apps.
type Publisher struct {
	ID            string
	Name          string
	GitHubOwner   string
	GitHubOwnerID int64
	OwnerType     string // "User" | "Organization"
	Homepage      string
	Verified      bool
	Official      bool
	Status        string // pending | active | suspended
	StatusReason  string
	CreatedBy     *int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	// The GitHub owner's profile when the publisher became active
	// (migration 0004): its text and its mirrored avatar ("" when none).
	Description  string
	AvatarSHA256 string
	AvatarType   string
}

const publisherCols = `p.id, p.name, p.github_owner, p.github_owner_id, p.owner_type, COALESCE(p.homepage, ''),
	p.verified, p.official, p.status, COALESCE(p.status_reason, ''), p.created_by, p.created_at, p.updated_at,
	COALESCE(p.description, ''), COALESCE(p.avatar_sha256, ''),
	COALESCE((SELECT m.content_type FROM media m WHERE m.sha256 = p.avatar_sha256), '')`

type scanner interface {
	Scan(dest ...any) error
}

func scanPublisher(row scanner) (Publisher, error) {
	var p Publisher
	err := row.Scan(&p.ID, &p.Name, &p.GitHubOwner, &p.GitHubOwnerID, &p.OwnerType, &p.Homepage,
		&p.Verified, &p.Official, &p.Status, &p.StatusReason, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt,
		&p.Description, &p.AvatarSHA256, &p.AvatarType)
	return p, err
}

// CreatePublisher inserts a pending publisher and makes the account its
// owner. ErrConflict when the id or the GitHub owner is already taken. Run
// it in a transaction.
func CreatePublisher(ctx context.Context, q Q, p Publisher, owner int64) (Publisher, error) {
	row := q.QueryRow(ctx, `
		INSERT INTO publishers AS p (id, name, github_owner, github_owner_id, owner_type, homepage, status, created_by)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), 'pending', $7)
		RETURNING `+publisherCols, p.ID, p.Name, p.GitHubOwner, p.GitHubOwnerID, p.OwnerType, p.Homepage, owner)
	out, err := scanPublisher(row)
	if err != nil {
		if isUnique(err) {
			return Publisher{}, ErrConflict
		}
		return Publisher{}, err
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO publisher_members (publisher_id, account_id, role) VALUES ($1, $2, 'owner')`, out.ID, owner); err != nil {
		return Publisher{}, err
	}
	return out, nil
}

// GetPublisher by id.
func GetPublisher(ctx context.Context, q Q, id string) (Publisher, error) {
	p, err := scanPublisher(q.QueryRow(ctx, `SELECT `+publisherCols+` FROM publishers p WHERE p.id = $1`, id))
	return p, notFound(err)
}

// ListPublishers lists publishers, pending first, then oldest first.
func ListPublishers(ctx context.Context, q Q, status string, page Page) ([]Publisher, int, error) {
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM publishers WHERE ($1 = '' OR status = $1)`, status).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.Query(ctx, `
		SELECT `+publisherCols+` FROM publishers p
		WHERE ($1 = '' OR p.status = $1)
		ORDER BY (p.status = 'pending') DESC, p.created_at, p.id
		LIMIT $2 OFFSET $3`, status, page.Limit(), page.Offset())
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Publisher{}
	for rows.Next() {
		p, err := scanPublisher(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// PublisherPatch is an administrator's change; nil fields stay.
type PublisherPatch struct {
	Status       *string
	StatusReason *string
	Verified     *bool
	Official     *bool
}

// UpdatePublisher applies the patch and returns the row before and after.
func UpdatePublisher(ctx context.Context, q Q, id string, patch PublisherPatch) (before, after Publisher, err error) {
	before, err = scanPublisher(q.QueryRow(ctx, `SELECT `+publisherCols+` FROM publishers p WHERE p.id = $1 FOR UPDATE`, id))
	if err != nil {
		return Publisher{}, Publisher{}, notFound(err)
	}
	after, err = scanPublisher(q.QueryRow(ctx, `
		UPDATE publishers AS p SET
			status = COALESCE($2, p.status),
			status_reason = CASE WHEN $3::text IS NULL THEN p.status_reason ELSE NULLIF($3, '') END,
			verified = COALESCE($4, p.verified),
			official = COALESCE($5, p.official),
			updated_at = now()
		WHERE p.id = $1
		RETURNING `+publisherCols, id, patch.Status, patch.StatusReason, patch.Verified, patch.Official))
	return before, after, err
}

// Membership is a publisher seen from one of its members.
type Membership struct {
	Publisher
	Role string // owner | maintainer
}

// MembershipsOf lists the publishers an account belongs to.
func MembershipsOf(ctx context.Context, q Q, accountID int64) ([]Membership, error) {
	rows, err := q.Query(ctx, `
		SELECT `+publisherCols+`, m.role FROM publisher_members m JOIN publishers p ON p.id = m.publisher_id
		WHERE m.account_id = $1 ORDER BY p.id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Membership{}
	for rows.Next() {
		var m Membership
		err := rows.Scan(&m.ID, &m.Name, &m.GitHubOwner, &m.GitHubOwnerID, &m.OwnerType, &m.Homepage,
			&m.Verified, &m.Official, &m.Status, &m.StatusReason, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt,
			&m.Description, &m.AvatarSHA256, &m.AvatarType, &m.Role)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MemberRole is the account's role in the publisher, "" when not a member.
func MemberRole(ctx context.Context, q Q, publisherID string, accountID int64) (string, error) {
	var role string
	err := q.QueryRow(ctx, `SELECT role FROM publisher_members WHERE publisher_id = $1 AND account_id = $2`,
		publisherID, accountID).Scan(&role)
	if err = notFound(err); err == ErrNotFound {
		return "", nil
	}
	return role, err
}

// MemberAccounts lists the accounts of a publisher.
func MemberAccounts(ctx context.Context, q Q, publisherID string) ([]int64, error) {
	rows, err := q.Query(ctx, `SELECT account_id FROM publisher_members WHERE publisher_id = $1`, publisherID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PublisherInIndex reports whether the index shows the publisher (it has an
// app with an approved version).
func PublisherInIndex(ctx context.Context, q Q, id string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM apps a WHERE a.publisher_id = $1
		               AND EXISTS (SELECT 1 FROM app_versions v WHERE v.app_id = a.id))`, id).Scan(&ok)
	return ok, err
}

// SetPublisherProfile records the GitHub owner's profile text and the
// mirrored avatar's sha256 ("" clears either).
func SetPublisherProfile(ctx context.Context, q Q, id, description, avatarSHA256 string) error {
	_, err := q.Exec(ctx, `UPDATE publishers SET description = NULLIF($2, ''), avatar_sha256 = NULLIF($3, ''), updated_at = now()
		WHERE id = $1`, id, description, avatarSHA256)
	return err
}

// PublicPublisher is a row of the public publisher list.
type PublicPublisher struct {
	Publisher
	Apps int // listed apps with an approved version
}

// PublicPublisherFilter narrows the public list.
type PublicPublisherFilter struct {
	Words []string // each matched against id, name, GitHub owner, description
	Sort  string   // "name" (default) | "apps"
}

// ListPublicPublishers lists the ACTIVE publishers with the number of their
// listed apps (unlisted and revoked apps, and apps without an approved
// version, are not counted), one page and the total.
func ListPublicPublishers(ctx context.Context, q Q, f PublicPublisherFilter, page Page) ([]PublicPublisher, int, error) {
	args := []any{}
	where := `p.status = 'active'`
	for _, w := range f.Words {
		args = append(args, likePattern(w))
		n := itoa(len(args))
		where += ` AND (p.id ILIKE $` + n + ` OR p.name ILIKE $` + n + ` OR p.github_owner ILIKE $` + n +
			` OR COALESCE(p.description, '') ILIKE $` + n + `)`
	}
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM publishers p WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	count := `(SELECT count(*) FROM apps a WHERE a.publisher_id = p.id AND a.status = 'listed'
		AND EXISTS (SELECT 1 FROM app_versions v WHERE v.app_id = a.id))`
	order := ` ORDER BY lower(p.name), p.id`
	if f.Sort == "apps" {
		order = ` ORDER BY apps DESC, lower(p.name), p.id`
	}
	args = append(args, page.Limit(), page.Offset())
	rows, err := q.Query(ctx, `SELECT `+publisherCols+`, `+count+` AS apps FROM publishers p WHERE `+where+order+
		` LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []PublicPublisher{}
	for rows.Next() {
		var r PublicPublisher
		err := rows.Scan(&r.ID, &r.Name, &r.GitHubOwner, &r.GitHubOwnerID, &r.OwnerType, &r.Homepage,
			&r.Verified, &r.Official, &r.Status, &r.StatusReason, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt,
			&r.Description, &r.AvatarSHA256, &r.AvatarType, &r.Apps)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}
