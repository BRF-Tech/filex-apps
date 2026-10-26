package index

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/config"
	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/signing"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// Key uses.
const (
	UseIndex    = "index"
	UseArtifact = "artifact"
	// UseLicense signs license verdicts (POST /v1/licenses/verify). Its key
	// is listed in /v1/keys.json, never in the index.
	UseLicense = "license"
)

// Keyring holds the private keys the server signs with. Any may be nil:
// without an index key no index is published (and no install intent is
// signed), without an artifact key nothing is approved, without a license
// key license checks answer 503.
type Keyring struct {
	Index    *signing.Key
	Artifact *signing.Key
	License  *signing.Key
}

// ErrNoKey is returned when the key a step needs is not configured.
var ErrNoKey = errors.New("signing key not configured")

// LoadKeys reads the keys from the configuration, or - in a demo
// (FAPPS_DEMO_KEYS=1 and APP_ENV demo or development) - loads or generates
// throw-away keys. Every key it uses is recorded in signing_keys as the
// active key of its use; a different key that was active is retired (key
// rotation with a `next` period is phase 2).
func LoadKeys(ctx context.Context, pool *pgxpool.Pool, cfg config.KeyConfig, env string, log *slog.Logger) (*Keyring, error) {
	kr := &Keyring{}
	demoAllowed := cfg.DemoKeys && (env == "demo" || env == "development")
	err := db.InTx(ctx, pool, func(tx pgx.Tx) error {
		if err := db.XactLock(ctx, tx, db.LockKeys); err != nil {
			return err
		}
		var err error
		if kr.Index, err = loadOne(ctx, tx, UseIndex, cfg.IndexKeyID, cfg.IndexKey, demoAllowed, log); err != nil {
			return err
		}
		if kr.Artifact, err = loadOne(ctx, tx, UseArtifact, cfg.ArtifactKeyID, cfg.ArtifactKey, demoAllowed, log); err != nil {
			return err
		}
		kr.License, err = loadOne(ctx, tx, UseLicense, cfg.LicenseKeyID, cfg.LicenseKey, demoAllowed, log)
		return err
	})
	if err != nil {
		return nil, err
	}
	return kr, nil
}

func loadOne(ctx context.Context, tx pgx.Tx, use, id, secret string, demoAllowed bool, log *slog.Logger) (*signing.Key, error) {
	if secret != "" && id != "" {
		k, err := signing.ParsePrivateKey(id, secret)
		if err != nil {
			return nil, err
		}
		if err := activate(ctx, tx, store.SigningKey{ID: id, Use: use, PublicHex: k.PublicHex()}, log); err != nil {
			return nil, err
		}
		return k, nil
	}
	if !demoAllowed {
		return nil, nil
	}
	active, err := store.ActiveKey(ctx, tx, use)
	switch {
	case err == nil && active.Demo:
		seed, err := store.DemoSeed(ctx, tx, active.ID)
		if err != nil {
			return nil, fmt.Errorf("demo %s key %s: %w", use, active.ID, err)
		}
		return signing.NewKeyFromSeed(active.ID, seed)
	case err == nil:
		return nil, fmt.Errorf("the active %s key %s is not a demo key and its private half is not configured", use, active.ID)
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	tmp, err := signing.NewKeyFromSeed("tmp", seed)
	if err != nil {
		return nil, err
	}
	keyID := "demo-" + use + "-" + tmp.PublicHex()[:12]
	k, err := signing.NewKeyFromSeed(keyID, seed)
	if err != nil {
		return nil, err
	}
	if err := store.InsertActiveKey(ctx, tx, store.SigningKey{ID: keyID, Use: use, PublicHex: k.PublicHex(), Demo: true}); err != nil {
		return nil, err
	}
	if err := store.InsertDemoSeed(ctx, tx, keyID, seed); err != nil {
		return nil, err
	}
	log.Warn("generated a throw-away demo key; no real filex trusts it", "use", use, "key_id", keyID)
	return k, nil
}

// activate records a configured key as the active key of its use.
func activate(ctx context.Context, tx pgx.Tx, k store.SigningKey, log *slog.Logger) error {
	existing, err := store.GetKey(ctx, tx, k.ID)
	switch {
	case err == nil:
		if existing.Use != k.Use || existing.PublicHex != k.PublicHex {
			return fmt.Errorf("signing key %q is recorded with another use or public key", k.ID)
		}
		if existing.Status == "active" {
			return nil
		}
		if existing.Status == "retired" {
			return fmt.Errorf("signing key %q was retired; a retired key is never used again", k.ID)
		}
	case !errors.Is(err, store.ErrNotFound):
		return err
	}
	if old, err := store.ActiveKey(ctx, tx, k.Use); err == nil {
		if err := store.RetireKey(ctx, tx, old.ID); err != nil {
			return err
		}
		log.Warn("retired the previous active key", "use", k.Use, "retired", old.ID, "active", k.ID)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if existing.ID != "" {
		return store.ActivateKey(ctx, tx, k.ID)
	}
	return store.InsertActiveKey(ctx, tx, k)
}
