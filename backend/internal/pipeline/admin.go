package pipeline

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/brf-tech/filex-apps/backend/internal/audit"
	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/notify"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// AppPatch is an administrator's listing change.
type AppPatch struct {
	Status     *string // listed | unlisted
	Categories *[]string
}

func lockApp(ctx context.Context, tx pgx.Tx, name string) (store.App, error) {
	a, err := store.LockApp(ctx, tx, name)
	if errors.Is(err, store.ErrNotFound) {
		return a, refuse(NotFound, "not_found", "No app %q.", name)
	}
	return a, err
}

// UpdateApp lists or unlists an app, or corrects its categories; a new
// index is published.
func (s *Service) UpdateApp(ctx context.Context, name string, p AppPatch, a Actor) (store.App, int64, error) {
	var app store.App
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		if app, err = lockApp(ctx, tx, name); err != nil {
			return err
		}
		data := map[string]any{}
		if p.Status != nil {
			if app.Status == "revoked" {
				return refuse(Conflict, "revoked", "The app is revoked; lift the revocation first.")
			}
			if err := store.SetAppStatus(ctx, tx, app.ID, *p.Status); err != nil {
				return err
			}
			data["status"] = map[string]string{"from": app.Status, "to": *p.Status}
		}
		if p.Categories != nil {
			cats, err := knownCategories(ctx, tx, *p.Categories)
			if err != nil {
				return err
			}
			if len(cats) != len(*p.Categories) {
				return refuse(Invalid, "invalid_field", "categories names an unknown category.")
			}
			if err := store.SetAppCategories(ctx, tx, app.ID, cats); err != nil {
				return err
			}
			data["categories"] = map[string]any{"from": app.Categories, "to": cats}
		}
		_, err = audit.Append(ctx, tx, s.entry(a, "app.update", "app", app.Name, data))
		return err
	})
	if err != nil {
		return store.App{}, 0, err
	}
	serial := s.publish(ctx, index.ReasonListing, a.AccountID)
	app, err = store.GetAppByID(ctx, s.Pool, app.ID)
	return app, serial, err
}

// Revoke marks an app harmful: it stays in the index with `revoked`, so
// installed copies are told to remove it.
func (s *Service) Revoke(ctx context.Context, name, reason string, a Actor) (store.App, int64, error) {
	return s.revocation(ctx, name, reason, a, true)
}

// Unrevoke lifts a revocation; the app is listed again.
func (s *Service) Unrevoke(ctx context.Context, name string, a Actor) (store.App, int64, error) {
	return s.revocation(ctx, name, "", a, false)
}

func (s *Service) revocation(ctx context.Context, name, reason string, a Actor, revoke bool) (store.App, int64, error) {
	var app store.App
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		if app, err = lockApp(ctx, tx, name); err != nil {
			return err
		}
		if revoke {
			if app.Status == "revoked" {
				return refuse(Conflict, "already_revoked", "The app is already revoked.")
			}
			if err := store.RevokeApp(ctx, tx, app.ID, reason); err != nil {
				return err
			}
			_, err = audit.Append(ctx, tx, s.entry(a, "app.revoke", "app", app.Name, map[string]any{"reason": reason}))
			return err
		}
		if app.Status != "revoked" {
			return refuse(Conflict, "not_revoked", "The app is not revoked.")
		}
		if err := store.UnrevokeApp(ctx, tx, app.ID); err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, s.entry(a, "app.unrevoke", "app", app.Name, map[string]any{"was": app.RevokedReason}))
		return err
	})
	if err != nil {
		return store.App{}, 0, err
	}
	reasonCode := index.ReasonUnrevoke
	if revoke {
		reasonCode = index.ReasonRevoke
	}
	serial := s.publish(ctx, reasonCode, a.AccountID)
	app, err = store.GetAppByID(ctx, s.Pool, app.ID)
	return app, serial, err
}

// Yank marks a version "do not install"; the index keeps it with `yanked`
// and `latest` skips it.
func (s *Service) Yank(ctx context.Context, name, version, reason string, a Actor) (store.App, store.Version, int64, error) {
	return s.yank(ctx, name, version, reason, a, true)
}

// Unyank lifts a yank.
func (s *Service) Unyank(ctx context.Context, name, version string, a Actor) (store.App, store.Version, int64, error) {
	return s.yank(ctx, name, version, "", a, false)
}

func (s *Service) yank(ctx context.Context, name, version, reason string, a Actor, yank bool) (store.App, store.Version, int64, error) {
	var app store.App
	var v store.Version
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		if app, err = lockApp(ctx, tx, name); err != nil {
			return err
		}
		v, err = store.GetVersion(ctx, tx, app.ID, version)
		if errors.Is(err, store.ErrNotFound) {
			return refuse(NotFound, "not_found", "%s has no version %s.", name, version)
		}
		if err != nil {
			return err
		}
		if yank {
			if v.YankedAt != nil {
				return refuse(Conflict, "already_yanked", "%s %s is already yanked.", name, version)
			}
			if err := store.YankVersion(ctx, tx, v.ID, reason, a.AccountID); err != nil {
				return err
			}
			_, err = audit.Append(ctx, tx, s.entry(a, "version.yank", "version", name+"@"+version, map[string]any{"reason": reason}))
			return err
		}
		if v.YankedAt == nil {
			return refuse(Conflict, "not_yanked", "%s %s is not yanked.", name, version)
		}
		if err := store.UnyankVersion(ctx, tx, v.ID); err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, s.entry(a, "version.unyank", "version", name+"@"+version, map[string]any{"was": v.YankedReason}))
		return err
	})
	if err != nil {
		return store.App{}, store.Version{}, 0, err
	}
	reasonCode := index.ReasonUnyank
	if yank {
		reasonCode = index.ReasonYank
	}
	serial := s.publish(ctx, reasonCode, a.AccountID)
	v, err = store.GetVersion(ctx, s.Pool, app.ID, version)
	return app, v, serial, err
}

// UpdatePublisher activates, suspends, verifies or marks a publisher
// official. Suspending ends its members' sessions. A new index is
// published when the index shows the publisher.
func (s *Service) UpdatePublisher(ctx context.Context, id string, p store.PublisherPatch, a Actor) (store.Publisher, int64, error) {
	var before, after store.Publisher
	var inIndex bool
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var upd store.Publisher
		var err error
		before, upd, err = store.UpdatePublisher(ctx, tx, id, p)
		if errors.Is(err, store.ErrNotFound) {
			return refuse(NotFound, "not_found", "No publisher %q.", id)
		}
		if err != nil {
			return err
		}
		after = upd
		if after.Status == "suspended" && before.Status != "suspended" {
			members, err := store.MemberAccounts(ctx, tx, id)
			if err != nil {
				return err
			}
			if _, err := store.DeleteSessionsOf(ctx, tx, members); err != nil {
				return err
			}
		}
		if inIndex, err = store.PublisherInIndex(ctx, tx, id); err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, s.entry(a, "publisher.update", "publisher", id, map[string]any{
			"status":   [2]string{before.Status, after.Status},
			"verified": [2]bool{before.Verified, after.Verified},
			"official": [2]bool{before.Official, after.Official},
			"reason":   after.StatusReason,
		}))
		return err
	})
	if err != nil {
		return store.Publisher{}, 0, err
	}
	var serial int64
	if inIndex {
		serial = s.publish(ctx, index.ReasonPublisher, a.AccountID)
	}
	if after.Status == "active" && before.Status != "active" {
		after = s.refreshProfile(ctx, after, a)
	}
	return after, serial, nil
}

// Owner is a GitHub account or organization as GitHub answered it.
type Owner struct {
	ID    int64
	Login string
	Type  string // User | Organization
}

// PublisherRequest asks for a publisher bound to a GitHub owner.
type PublisherRequest struct {
	Actor        Actor
	GitHubUserID int64 // the signed-in GitHub identity's numeric id
	ID           string
	Name         string
	Homepage     string
	Owner        Owner
}

// RequestPublisher creates a pending publisher bound to the owner's numeric
// id, with the requester as its owner member. A user owner must be the
// requester; an organization is confirmed by an administrator before it is
// activated (no read:org scope is asked).
func (s *Service) RequestPublisher(ctx context.Context, in PublisherRequest) (store.Publisher, error) {
	if in.Owner.Type == "User" && in.Owner.ID != in.GitHubUserID {
		return store.Publisher{}, refuse(Forbidden, "not_owner", "A publisher for the GitHub user %s is asked for by that user.", in.Owner.Login)
	}
	var out store.Publisher
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		out, err = store.CreatePublisher(ctx, tx, store.Publisher{
			ID: in.ID, Name: in.Name, GitHubOwner: in.Owner.Login, GitHubOwnerID: in.Owner.ID,
			OwnerType: in.Owner.Type, Homepage: in.Homepage,
		}, in.Actor.AccountID)
		if errors.Is(err, store.ErrConflict) {
			return refuse(Conflict, "publisher_exists", "The publisher id or the GitHub owner %s is already taken.", in.Owner.Login)
		}
		if err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, s.entry(in.Actor, "publisher.request", "publisher", out.ID, map[string]any{
			"github_owner": in.Owner.Login, "github_owner_id": in.Owner.ID, "owner_type": in.Owner.Type}))
		if err != nil {
			return err
		}
		// The administrators hear of it: a publisher waits for them.
		_, login, err := store.GitHubIdentity(ctx, tx, in.Actor.AccountID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		_, err = notify.Emit(ctx, tx, notify.PublisherRequestEvent(out, login))
		return err
	})
	return out, err
}

// PublishNow re-signs the index (same content, serial + 1, new expiry).
func (s *Service) PublishNow(ctx context.Context, a Actor) (*store.Release, error) {
	if s.Index == nil {
		return nil, refuse(Unavailable, "signing_unavailable", "No index key is configured.")
	}
	actor := a.AccountID
	res, err := s.Index.Publish(ctx, index.ReasonRefresh, &actor, true)
	if errors.Is(err, index.ErrNoKey) {
		return nil, refuse(Unavailable, "signing_unavailable", "No index key is configured.")
	}
	if err != nil {
		return nil, err
	}
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		_, err := audit.Append(ctx, tx, s.entry(a, "index.publish", "index", res.Release.Serial, map[string]any{"sha256": res.Release.SHA256}))
		return err
	})
	return res.Release, err
}
