package store

import (
	"context"
	"strings"
	"time"
)

// App is one catalog entry.
type App struct {
	ID            int64
	Name          string
	Kind          string
	PublisherID   string
	RepoOwner     string
	RepoName      string
	RepoID        int64
	Homepage      string
	License       string
	Categories    []string
	Label         map[string]string
	Summary       map[string]string
	DescriptionMD string
	IconSHA256    string
	Status        string // listed | unlisted | revoked
	RevokedReason string
	RevokedAt     *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
	// The price, display only (migration 0006): "free" or "paid"; a paid
	// app may name its price text and where to buy it.
	PricingModel string
	PriceText    string
	PurchaseURL  string
}

// Repo is "owner/name".
func (a App) Repo() string { return a.RepoOwner + "/" + a.RepoName }

const appCols = `a.id, a.name, a.kind, a.publisher_id, a.repo_owner, a.repo_name, a.repo_id,
	COALESCE(a.homepage, ''), COALESCE(a.license, ''), a.categories, a.label, a.summary,
	COALESCE(a.description_md, ''), COALESCE(a.icon_sha256, ''), a.status, COALESCE(a.revoked_reason, ''),
	a.revoked_at, a.created_at, a.updated_at, a.pricing_model, COALESCE(a.price_text, ''), COALESCE(a.purchase_url, '')`

func scanApp(row scanner, extra ...any) (App, error) {
	var a App
	dest := []any{&a.ID, &a.Name, &a.Kind, &a.PublisherID, &a.RepoOwner, &a.RepoName, &a.RepoID,
		&a.Homepage, &a.License, &a.Categories, &a.Label, &a.Summary,
		&a.DescriptionMD, &a.IconSHA256, &a.Status, &a.RevokedReason,
		&a.RevokedAt, &a.CreatedAt, &a.UpdatedAt, &a.PricingModel, &a.PriceText, &a.PurchaseURL}
	err := row.Scan(append(dest, extra...)...)
	a.Categories = emptyIfNil(a.Categories)
	return a, err
}

// GetAppByName finds an app by its (global) name.
func GetAppByName(ctx context.Context, q Q, name string) (App, error) {
	a, err := scanApp(q.QueryRow(ctx, `SELECT `+appCols+` FROM apps a WHERE a.name = $1`, name))
	return a, notFound(err)
}

// GetAppByRepoID finds the app bound to a GitHub repository id.
func GetAppByRepoID(ctx context.Context, q Q, repoID int64) (App, error) {
	a, err := scanApp(q.QueryRow(ctx, `SELECT `+appCols+` FROM apps a WHERE a.repo_id = $1`, repoID))
	return a, notFound(err)
}

// GetAppByID finds an app by its row id.
func GetAppByID(ctx context.Context, q Q, id int64) (App, error) {
	a, err := scanApp(q.QueryRow(ctx, `SELECT `+appCols+` FROM apps a WHERE a.id = $1`, id))
	return a, notFound(err)
}

// LockApp reads an app FOR UPDATE.
func LockApp(ctx context.Context, q Q, name string) (App, error) {
	a, err := scanApp(q.QueryRow(ctx, `SELECT `+appCols+` FROM apps a WHERE a.name = $1 FOR UPDATE`, name))
	return a, notFound(err)
}

// InsertApp creates the catalog entry at a first approval. ErrConflict when
// the name or the repository is already bound.
func InsertApp(ctx context.Context, q Q, a App) (int64, error) {
	var id int64
	err := q.QueryRow(ctx, `
		INSERT INTO apps (name, kind, publisher_id, repo_owner, repo_name, repo_id, homepage, license,
		                  categories, label, summary, description_md, icon_sha256, status)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), NULLIF($8, ''), $9, $10, $11, NULLIF($12, ''), NULLIF($13, ''), 'listed')
		RETURNING id`,
		a.Name, a.Kind, a.PublisherID, a.RepoOwner, a.RepoName, a.RepoID, a.Homepage, a.License,
		emptyIfNil(a.Categories), textOrEmpty(a.Label), textOrEmpty(a.Summary), a.DescriptionMD, a.IconSHA256).Scan(&id)
	if err != nil && isUnique(err) {
		return 0, ErrConflict
	}
	return id, err
}

// UpdateAppListing replaces the listing from a newly approved version
// (repository names, homepage, license, categories, label, summary,
// description, icon). Status and revocation are not touched, and neither is
// the kind: a listing keeps the kind it was created with, and a.Kind must be
// that kind - another (or no such app) answers ErrConflict and changes
// nothing.
func UpdateAppListing(ctx context.Context, q Q, a App) error {
	tag, err := q.Exec(ctx, `
		UPDATE apps SET repo_owner = $3, repo_name = $4, homepage = NULLIF($5, ''), license = NULLIF($6, ''),
		       categories = $7, label = $8, summary = $9, description_md = NULLIF($10, ''), icon_sha256 = NULLIF($11, ''),
		       updated_at = now()
		WHERE id = $1 AND kind = $2`,
		a.ID, a.Kind, a.RepoOwner, a.RepoName, a.Homepage, a.License, emptyIfNil(a.Categories),
		textOrEmpty(a.Label), textOrEmpty(a.Summary), a.DescriptionMD, a.IconSHA256)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// SetAppStatus lists or unlists an app (not a revoked one).
func SetAppStatus(ctx context.Context, q Q, id int64, status string) error {
	_, err := q.Exec(ctx, `UPDATE apps SET status = $2, updated_at = now() WHERE id = $1 AND status <> 'revoked'`, id, status)
	return err
}

// SetAppCategories corrects an app's categories.
func SetAppCategories(ctx context.Context, q Q, id int64, categories []string) error {
	_, err := q.Exec(ctx, `UPDATE apps SET categories = $2, updated_at = now() WHERE id = $1`, id, emptyIfNil(categories))
	return err
}

// RevokeApp marks an app harmful.
func RevokeApp(ctx context.Context, q Q, id int64, reason string) error {
	_, err := q.Exec(ctx, `
		UPDATE apps SET status = 'revoked', revoked_reason = $2, revoked_at = now(), updated_at = now()
		WHERE id = $1`, id, reason)
	return err
}

// UnrevokeApp lifts a revocation; the app is listed again.
func UnrevokeApp(ctx context.Context, q Q, id int64) error {
	_, err := q.Exec(ctx, `
		UPDATE apps SET status = 'listed', revoked_reason = NULL, revoked_at = NULL, updated_at = now()
		WHERE id = $1`, id)
	return err
}

// AppMedia is one of an app's screenshots.
type AppMedia struct {
	SHA256      string
	ContentType string
	Position    int
	Caption     map[string]string
}

// ReplaceAppMedia replaces an app's screenshots.
func ReplaceAppMedia(ctx context.Context, q Q, appID int64, shots []AppMedia) error {
	if _, err := q.Exec(ctx, `DELETE FROM app_media WHERE app_id = $1`, appID); err != nil {
		return err
	}
	for i, s := range shots {
		if _, err := q.Exec(ctx, `INSERT INTO app_media (app_id, sha256, position, caption) VALUES ($1, $2, $3, $4)`,
			appID, s.SHA256, i, textOrEmpty(s.Caption)); err != nil {
			return err
		}
	}
	return nil
}

// AppScreenshots lists an app's screenshots in order.
func AppScreenshots(ctx context.Context, q Q, appID int64) ([]AppMedia, error) {
	rows, err := q.Query(ctx, `
		SELECT am.sha256, m.content_type, am.position, am.caption
		FROM app_media am JOIN media m ON m.sha256 = am.sha256
		WHERE am.app_id = $1 ORDER BY am.position`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppMedia{}
	for rows.Next() {
		var m AppMedia
		if err := rows.Scan(&m.SHA256, &m.ContentType, &m.Position, &m.Caption); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AppNames lists every app's name with its status.
func AppNames(ctx context.Context, q Q) (map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT name, status FROM apps`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, s string
		if err := rows.Scan(&n, &s); err != nil {
			return nil, err
		}
		out[n] = s
	}
	return out, rows.Err()
}

// ReservedName reports whether a name is reserved, and why.
func ReservedName(ctx context.Context, q Q, name string) (bool, string, error) {
	var reason string
	err := q.QueryRow(ctx, `SELECT reason FROM reserved_names WHERE name = $1`, strings.ToLower(name)).Scan(&reason)
	if err = notFound(err); err == ErrNotFound {
		return false, "", nil
	}
	return err == nil, reason, err
}

// Category is a fixed category id and its label.
type Category struct {
	ID    string
	Label map[string]string
}

// ListCategories in display order.
func ListCategories(ctx context.Context, q Q) ([]Category, error) {
	rows, err := q.Query(ctx, `SELECT id, label FROM categories ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Label); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PublisherRef is the short form of a publisher shown with an app, with its
// mirrored avatar (migration 0004; "" when it has none) and that image's type.
type PublisherRef struct {
	ID           string
	Name         string
	Verified     bool
	Official     bool
	AvatarSHA256 string
	AvatarType   string
}

// VersionBrief is what the catalog needs of a version.
type VersionBrief struct {
	ID          int64
	Version     string
	FilexRange  string
	Yanked      bool
	PublishedAt time.Time
}

// CatalogApp is an app with its publisher and its versions' briefs.
type CatalogApp struct {
	App
	Publisher PublisherRef
	IconType  string
	Versions  []VersionBrief
}

// CatalogFilter narrows the catalog in SQL; the filex filter, sorting and
// paging are done by the caller (they need version order).
type CatalogFilter struct {
	Words       []string
	Kind        string
	Category    string
	Publisher   string
	Verified    bool
	Statuses    []string // default: listed only
	WithVersion bool     // only apps with at least one approved version
	// Member keeps the apps of the publishers this account belongs to (0:
	// every app).
	Member int64
}

// CatalogApps returns the apps matching the filter, with their versions.
func CatalogApps(ctx context.Context, q Q, f CatalogFilter) ([]CatalogApp, error) {
	statuses := f.Statuses
	if len(statuses) == 0 {
		statuses = []string{"listed"}
	}
	args := []any{statuses, f.Kind, f.Category, f.Publisher, f.Verified}
	where := `a.status = ANY($1) AND ($2 = '' OR a.kind = $2) AND ($3 = '' OR $3 = ANY(a.categories))
		AND ($4 = '' OR a.publisher_id = $4) AND (NOT $5 OR p.verified)`
	if f.WithVersion {
		where += ` AND EXISTS (SELECT 1 FROM app_versions v WHERE v.app_id = a.id)`
	}
	if f.Member != 0 {
		args = append(args, f.Member)
		where += ` AND EXISTS (SELECT 1 FROM publisher_members pm WHERE pm.publisher_id = a.publisher_id AND pm.account_id = $` +
			itoa(len(args)) + `)`
	}
	for _, w := range f.Words {
		args = append(args, likePattern(w))
		n := len(args)
		where += ` AND (a.name ILIKE $` + itoa(n) + ` OR a.label::text ILIKE $` + itoa(n) +
			` OR a.summary::text ILIKE $` + itoa(n) + ` OR p.name ILIKE $` + itoa(n) + ` OR p.id ILIKE $` + itoa(n) + `)`
	}
	rows, err := q.Query(ctx, `
		SELECT `+appCols+`, p.id, p.name, p.verified, p.official, COALESCE(m.content_type, ''),
			COALESCE(p.avatar_sha256, ''), COALESCE(pm.content_type, '')
		FROM apps a JOIN publishers p ON p.id = a.publisher_id
		LEFT JOIN media m ON m.sha256 = a.icon_sha256
		LEFT JOIN media pm ON pm.sha256 = p.avatar_sha256
		WHERE `+where+` ORDER BY a.name`, args...)
	if err != nil {
		return nil, err
	}
	var out []CatalogApp
	byID := map[int64]int{}
	for rows.Next() {
		var c CatalogApp
		app, err := scanApp(rows, &c.Publisher.ID, &c.Publisher.Name, &c.Publisher.Verified, &c.Publisher.Official, &c.IconType,
			&c.Publisher.AvatarSHA256, &c.Publisher.AvatarType)
		if err != nil {
			rows.Close()
			return nil, err
		}
		c.App = app
		byID[app.ID] = len(out)
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return []CatalogApp{}, nil
	}
	ids := make([]int64, 0, len(out))
	for _, c := range out {
		ids = append(ids, c.ID)
	}
	vrows, err := q.Query(ctx, `
		SELECT app_id, id, version, filex_range, yanked_at IS NOT NULL, published_at
		FROM app_versions WHERE app_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer vrows.Close()
	for vrows.Next() {
		var id int64
		var v VersionBrief
		if err := vrows.Scan(&id, &v.ID, &v.Version, &v.FilexRange, &v.Yanked, &v.PublishedAt); err != nil {
			return nil, err
		}
		i := byID[id]
		out[i].Versions = append(out[i].Versions, v)
	}
	return out, vrows.Err()
}

// AppsOfPublisher lists a publisher's apps in the given statuses (with at
// least one version).
func AppsOfPublisher(ctx context.Context, q Q, publisherID string, statuses []string) ([]CatalogApp, error) {
	return CatalogApps(ctx, q, CatalogFilter{Publisher: publisherID, Statuses: statuses, WithVersion: true})
}

func textOrEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
