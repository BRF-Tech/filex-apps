// Package pipeline moves a submission from "queued" to "needs_review"
// (docs/ARCHITECTURE.md "Submission pipeline"). The backend lane owns the
// state machine, the job queue and the storage; the validator lane owns the
// two ports below and nothing else in this package.
//
// ⚠ CONTRACT: the interfaces here are what the pipeline calls and the
// resolver and validator packages implement. Change them together.
package pipeline

import (
	"context"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
)

// Resolver establishes what a repository serves at a tag: the repository
// check, the tag's commit, the manifest and its sha256, the consent file,
// the artifacts' sizes and hashes, the mirrored media. It downloads only
// through the guarded client (internal/safehttp) and only from the allow
// list. It returns every check it ran, passed or not; err is for the
// pipeline's own failures (a network error worth retrying), not for a
// submission that fails a check.
type Resolver interface {
	Resolve(ctx context.Context, req domain.ResolveRequest) (*domain.Resolution, []domain.CheckResult, error)
}

// MediaSink stores an image the resolver fetched and checked (the backend
// lane implements it in internal/media; the resolver only calls it). Put is
// idempotent: the same sha256 twice is one row.
type MediaSink interface {
	Put(ctx context.Context, m domain.MediaRef, sourceURL string, body []byte) error
}

// DryRunner asks every configured filex (internal/validator) for its install
// review of the version, without installing, and compares each answer with
// the resolver's pins. One CheckResult per validator.
type DryRunner interface {
	DryRun(ctx context.Context, req domain.DryRunRequest) ([]domain.DryRunResult, []domain.CheckResult, error)
}
