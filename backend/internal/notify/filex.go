package notify

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/filexrange"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// stateFilexLatest is the notification_state key of the newest filex
// release the store has announced.
const stateFilexLatest = "filex_latest"

// LatestRelease is the newest filex release among versions the store knows:
// its own file-type data (filex-builtin.json's "0.57.3") and what its
// validators report ("v0.57.3 (6ca679fb..., 2026-10-19T...)"). A
// pre-release or a development build is no release ("" when none is).
func LatestRelease(reports []string) string {
	var best filexrange.Version
	found := false
	for _, r := range reports {
		f := strings.Fields(r)
		if len(f) == 0 {
			continue
		}
		v, err := filexrange.ParseVersion(f[0])
		if err != nil || v.Pre != "" {
			continue
		}
		if !found || v.Compare(best) > 0 {
			best, found = v, true
		}
	}
	if !found {
		return ""
	}
	return best.Plain()
}

// NoteFilexVersions compares the newest release among reports with the one
// the store last announced and raises KindFilexReleased once when it is
// newer. The first time (nothing announced yet) it only remembers the
// version: a store that starts is not news. It returns the version it
// announced ("" for none).
//
// The sources are only what the store already knows (the embedded
// filex-builtin.json, the validators' own versions): a new release reaches
// the store when the operator deploys an image or updates a validator, and
// the next check after that announces it. The store never asks GitHub.
func (s *Service) NoteFilexVersions(ctx context.Context, reports []string) (string, error) {
	latest := LatestRelease(reports)
	if latest == "" {
		return "", nil
	}
	cur, err := filexrange.ParseVersion(latest)
	if err != nil {
		return "", err
	}
	var announced string
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('fapps:notify:filex_latest'))`); err != nil {
			return err
		}
		prev, found, err := store.NotifyState(ctx, tx, stateFilexLatest)
		if err != nil {
			return err
		}
		if !found {
			return store.SetNotifyState(ctx, tx, stateFilexLatest, latest)
		}
		pv, err := filexrange.ParseVersion(prev)
		if err == nil && cur.Compare(pv) <= 0 {
			return nil
		}
		if _, err := Emit(ctx, tx, FilexReleasedEvent(latest, prev)); err != nil {
			return err
		}
		announced = latest
		return store.SetNotifyState(ctx, tx, stateFilexLatest, latest)
	})
	return announced, err
}
