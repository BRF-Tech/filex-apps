package pipeline

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/brf-tech/filex-apps/backend/internal/audit"
	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// ProfileReader reads a GitHub owner's public profile for the publisher
// list: its profile text and its avatar, which the implementation mirrors
// through the media sink (same checks as any listing image) before
// answering. cmd/server implements it on the GitHub reader.
type ProfileReader interface {
	Profile(ctx context.Context, ownerID int64, login string) (Profile, error)
}

// Profile is what a ProfileReader found. AvatarSHA256 is "" when the owner
// has no avatar or it could not be mirrored.
type Profile struct {
	Description  string
	AvatarSHA256 string
}

// MaxPublisherDescription caps the profile text kept (migration 0004).
const MaxPublisherDescription = 500

// RefreshPublisherProfile reads the publisher's GitHub profile again and
// keeps its text and mirrored avatar (the operator command; activation does
// it by itself).
func (s *Service) RefreshPublisherProfile(ctx context.Context, id string, a Actor) (store.Publisher, error) {
	pub, err := store.GetPublisher(ctx, s.Pool, id)
	if err != nil {
		return store.Publisher{}, err
	}
	if s.Profiles == nil {
		return pub, nil
	}
	prof, err := s.Profiles.Profile(ctx, pub.GitHubOwnerID, pub.GitHubOwner)
	if err != nil {
		return pub, err
	}
	return s.keepProfile(ctx, pub, prof, a)
}

// refreshProfile is RefreshPublisherProfile on a publisher that just became
// active: best effort, a failure is logged and the publisher stays as it is.
func (s *Service) refreshProfile(ctx context.Context, pub store.Publisher, a Actor) store.Publisher {
	if s.Profiles == nil {
		return pub
	}
	prof, err := s.Profiles.Profile(ctx, pub.GitHubOwnerID, pub.GitHubOwner)
	if err != nil {
		s.log().Warn("publisher profile not read", "publisher", pub.ID, "err", err)
		return pub
	}
	out, err := s.keepProfile(ctx, pub, prof, a)
	if err != nil {
		s.log().Warn("publisher profile not kept", "publisher", pub.ID, "err", err)
		return pub
	}
	return out
}

func (s *Service) keepProfile(ctx context.Context, pub store.Publisher, prof Profile, a Actor) (store.Publisher, error) {
	desc := strings.TrimSpace(prof.Description)
	if utf8.RuneCountInString(desc) > MaxPublisherDescription {
		r := []rune(desc)
		desc = strings.TrimSpace(string(r[:MaxPublisherDescription-3])) + "..."
	}
	if desc == pub.Description && prof.AvatarSHA256 == pub.AvatarSHA256 {
		return pub, nil
	}
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := store.SetPublisherProfile(ctx, tx, pub.ID, desc, prof.AvatarSHA256); err != nil {
			return err
		}
		_, err := audit.Append(ctx, tx, s.entry(a, "publisher.profile", "publisher", pub.ID, map[string]any{
			"description": desc, "avatar_sha256": prof.AvatarSHA256}))
		return err
	})
	if err != nil {
		return pub, err
	}
	return store.GetPublisher(ctx, s.Pool, pub.ID)
}
