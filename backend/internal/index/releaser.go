package index

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/signing"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// Release reasons (the index_releases CHECK).
const (
	ReasonInitial     = "initial"
	ReasonApprove     = "approve"
	ReasonYank        = "yank"
	ReasonUnyank      = "unyank"
	ReasonRevoke      = "revoke"
	ReasonUnrevoke    = "unrevoke"
	ReasonPublisher   = "publisher"
	ReasonListing     = "listing"
	ReasonRefresh     = "refresh"
	ReasonKeyRotation = "key_rotation"
)

// Releaser builds, signs, stores and serves the index. It is the only
// writer of index_releases.
type Releaser struct {
	Pool          *pgxpool.Pool
	Keys          *Keyring
	PublicURL     string
	TTL           time.Duration
	RefreshBefore time.Duration
	Log           *slog.Logger
	Now           func() time.Time

	mu     sync.Mutex
	latest *store.Release
}

// Result is what a publish did.
type Result struct {
	Release   *store.Release
	Published bool // false: the content had not changed, Release is the latest
}

func (p *Releaser) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Publish builds the index from the committed state and, when it differs
// from the latest release (or force is set), signs it and stores it as
// serial = latest + 1, under a transaction-level advisory lock so two
// publishers never take the same serial.
func (p *Releaser) Publish(ctx context.Context, reason string, actor *int64, force bool) (Result, error) {
	if p.Keys == nil || p.Keys.Index == nil {
		return Result{}, ErrNoKey
	}
	var res Result
	err := db.InTx(ctx, p.Pool, func(tx pgx.Tx) error {
		if err := db.XactLock(ctx, tx, db.LockIndex); err != nil {
			return err
		}
		latest, err := store.LatestRelease(ctx, tx)
		if err != nil {
			return err
		}
		now := p.now()
		snap, err := store.LoadIndexSnapshot(ctx, tx, now.Add(-p.TTL))
		if err != nil {
			return err
		}
		doc := Build(snap, p.PublicURL)
		if !force && latest != nil && sameContent(latest.Body, doc) {
			res = Result{Release: latest}
			return nil
		}
		var serial int64 = 1
		if latest != nil {
			serial = latest.Serial + 1
		}
		rel, err := Sign(&doc, serial, now, p.TTL, p.Keys.Index)
		if err != nil {
			return err
		}
		rel.Reason, rel.CreatedBy = reason, actor
		if err := store.InsertRelease(ctx, tx, rel); err != nil {
			return err
		}
		res = Result{Release: &rel, Published: true}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	if res.Published {
		p.mu.Lock()
		p.latest = res.Release
		p.mu.Unlock()
		p.log().Info("index published", "serial", res.Release.Serial, "reason", reason, "sha256", res.Release.SHA256)
	}
	return res, nil
}

// Sign stamps the document, writes its canonical bytes and signs them with
// the index key.
func Sign(doc *Document, serial int64, now time.Time, ttl time.Duration, key *signing.Key) (store.Release, error) {
	Stamp(doc, serial, now, ttl)
	body, err := Canonical(doc)
	if err != nil {
		return store.Release{}, err
	}
	gen, _ := time.Parse(TimeLayout, doc.GeneratedAt)
	exp, _ := time.Parse(TimeLayout, doc.ExpiresAt)
	return store.Release{
		Serial:      serial,
		GeneratedAt: gen,
		ExpiresAt:   exp,
		SHA256:      signing.SHA256Hex(body),
		Signature:   key.SignBytes(body),
		KeyID:       key.ID,
		Body:        body,
	}, nil
}

// EnsureFresh publishes the first index when there is none, and re-signs
// the same content (serial + 1) when less than RefreshBefore is left before
// the latest one expires. A sleeping demo calls it when it wakes.
func (p *Releaser) EnsureFresh(ctx context.Context) (Result, error) {
	latest, err := store.LatestRelease(ctx, p.Pool)
	if err != nil {
		return Result{}, err
	}
	switch {
	case latest == nil:
		return p.Publish(ctx, ReasonInitial, nil, true)
	case latest.ExpiresAt.Sub(p.now()) < p.RefreshBefore:
		return p.Publish(ctx, ReasonRefresh, nil, true)
	}
	return Result{Release: latest}, nil
}

// Latest returns the newest release (cached; one cheap query checks that
// the cache is current). nil when none exists yet.
func (p *Releaser) Latest(ctx context.Context) (*store.Release, error) {
	serial, err := store.LatestSerial(ctx, p.Pool)
	if err != nil {
		return nil, err
	}
	if serial == 0 {
		return nil, nil
	}
	p.mu.Lock()
	cached := p.latest
	p.mu.Unlock()
	if cached != nil && cached.Serial == serial {
		return cached, nil
	}
	rel, err := store.GetRelease(ctx, p.Pool, serial)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.latest == nil || p.latest.Serial < rel.Serial {
		p.latest = &rel
	}
	p.mu.Unlock()
	return &rel, nil
}

func (p *Releaser) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}
