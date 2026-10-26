package install

// Instance connections (docs/ARCHITECTURE.md "Instance connections"): a
// filex SERVER asks for install intents in its own name, for
// the instance a person saved here and connected to it.
//
//	the person, on the store        the filex's administrator, on filex
//	My instances > Connect  ---->   pastes the code (shown once, 30 minutes)
//	                                filex makes an ed25519 key for this store
//	POST /v1/instances/connect  <-  {code, public_key, filex_origin, filex_instance_id?}
//	-> signed (index key): {store, instance_id, filex_origin, key_fingerprint, connected_at}
//
//	POST /v1/instances/{id}/intents  <- a signed request (RequestSignature)
//	-> {token, token_id, expires_at, app, version}; filex reads the intent at
//	   GET /v1/install/{token} like any other and holds it to its own review
//
// The store keeps the filex's PUBLIC key only, so nothing it holds can sign
// as the filex. A request is held to the key, the instance it names, its
// method, path and body, a timestamp within RequestWindow and a nonce seen
// once (instance_request_nonces).
//
// An intent asked for this way is an intent like a person's: one-time, 30
// minutes, sealed and bound to the instance's own address (filex_origin), so
// a filex refuses it anywhere else. It names the instance's owner as its
// account. A license key goes in only when the filex's administrator gave
// one at the approval, and only for a license the instance holds a seat of
// or can take one of.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/brf-tech/filex-apps/backend/internal/audit"
	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/licensing"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/signedjson"
	"github.com/brf-tech/filex-apps/backend/internal/signing"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// ConnectCodeTTL is how long a connection code waits for its filex.
const ConnectCodeTTL = 30 * time.Minute

// MaxLiveConnectCodes is how many unused codes one instance may have at once.
const MaxLiveConnectCodes = 5

// RequestWindow is how far a signed request's timestamp may be from the
// store's clock, either way. Nonces are kept for twice that.
const RequestWindow = 5 * time.Minute

// ConnectCodePrefix starts every connection code (a person can tell it from
// an install link's token at a glance).
const ConnectCodePrefix = "fxc_"

// Request headers of a connected filex's signed request.
const (
	HeaderInstance  = "Fapps-Instance"
	HeaderKey       = "Fapps-Key"
	HeaderTimestamp = "Fapps-Timestamp"
	HeaderNonce     = "Fapps-Nonce"
	HeaderSignature = "Fapps-Signature"
)

// requestScheme is the first line of the text a request signs: the scheme's
// name and version. A change of what is signed is a new version.
const requestScheme = "FAPPS-INSTANCE-REQUEST-1"

var (
	connectCodeRe = regexp.MustCompile(`^fxc_[A-Za-z0-9_-]{43}$`)
	nonceRe       = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
	fprRe         = regexp.MustCompile(`^[0-9a-f]{64}$`)
	sigRe         = regexp.MustCompile(`^[0-9a-f]{128}$`)
	tsRe          = regexp.MustCompile(`^[0-9]{1,12}$`)
)

// KeyFingerprint is a public key's fingerprint: the lower-hex sha256 of its
// 32 raw bytes (filex's appstore.Key.Fingerprint).
func KeyFingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// RequestText is what a connected filex signs for one request: the scheme,
// the method, the path (as sent, without a query), the instance, the key's
// fingerprint, the timestamp (Unix seconds), the nonce and the lower-hex
// sha256 of the body (of nothing for an empty body), one per line.
func RequestText(method, path, instanceID, keyFingerprint, timestamp, nonce string, body []byte) string {
	sum := sha256.Sum256(body)
	return strings.Join([]string{requestScheme, strings.ToUpper(method), path, instanceID, keyFingerprint, timestamp, nonce,
		hex.EncodeToString(sum[:])}, "\n")
}

// SignRequest signs a request the way a filex does: the key's ed25519 over
// the lower-hex sha256 of RequestText (the store's one signature rule,
// internal/signing). For tests and the e2e run's client.
func SignRequest(priv ed25519.PrivateKey, method, path, instanceID, timestamp, nonce string, body []byte) string {
	fpr := KeyFingerprint(priv.Public().(ed25519.PublicKey))
	text := RequestText(method, path, instanceID, fpr, timestamp, nonce, body)
	return hex.EncodeToString(ed25519.Sign(priv, []byte(signing.SHA256Hex([]byte(text)))))
}

// SignedRequest is what a signed request's headers say.
type SignedRequest struct {
	Method     string
	Path       string
	InstanceID string
	Key        string // fingerprint
	Timestamp  string
	Nonce      string
	Signature  string
	Body       []byte
}

// errUnauthorized is the one answer for a request whose instance, key or
// signature does not hold: which of them is not said.
var errUnauthorized = refuse(pipeline.Unauthorized, "instance_unauthorized",
	"The request is not signed by a filex connected to this instance.")

// VerifyRequest checks a connected filex's signed request and answers the
// instance it is for. pathID is the instance the path names: it must be the
// one the headers name. The nonce is spent only after the signature holds,
// so nobody but the key's holder can use one up.
func (s *Service) VerifyRequest(ctx context.Context, req SignedRequest, pathID string) (store.SavedInstance, error) {
	if !uuidRe.MatchString(req.InstanceID) || req.InstanceID != pathID || !fprRe.MatchString(req.Key) ||
		!tsRe.MatchString(req.Timestamp) || !nonceRe.MatchString(req.Nonce) || !sigRe.MatchString(req.Signature) {
		return store.SavedInstance{}, errUnauthorized
	}
	ts, err := strconv.ParseInt(req.Timestamp, 10, 64)
	if err != nil {
		return store.SavedInstance{}, errUnauthorized
	}
	if d := s.now().Sub(time.Unix(ts, 0)); d > RequestWindow || d < -RequestWindow {
		return store.SavedInstance{}, refuse(pipeline.Unauthorized, "request_expired",
			"The request's timestamp is more than %d minutes from the store's clock; check the filex server's clock.", int(RequestWindow/time.Minute))
	}
	inst, err := store.InstanceByID(ctx, s.Pool, req.InstanceID)
	if errors.Is(err, store.ErrNotFound) {
		return store.SavedInstance{}, errUnauthorized
	}
	if err != nil {
		return store.SavedInstance{}, err
	}
	if !inst.Connected() || inst.ServerKeyFingerprint != req.Key {
		return store.SavedInstance{}, errUnauthorized
	}
	text := RequestText(req.Method, req.Path, req.InstanceID, req.Key, req.Timestamp, req.Nonce, req.Body)
	if err := signing.Verify([]ed25519.PublicKey{ed25519.PublicKey(inst.ServerKey)}, signing.SHA256Hex([]byte(text)), req.Signature); err != nil {
		return store.SavedInstance{}, errUnauthorized
	}
	fresh, err := store.SeeNonce(ctx, s.Pool, inst.ID, req.Nonce, 2*RequestWindow)
	if err != nil {
		return store.SavedInstance{}, err
	}
	if !fresh {
		return store.SavedInstance{}, refuse(pipeline.Unauthorized, "request_replayed", "This request was sent before.")
	}
	if err := store.TouchInstance(ctx, s.Pool, inst.ID); err != nil {
		s.log().Warn("a connected instance's request time was not recorded", "id", inst.ID, "err", err)
	}
	return inst, nil
}

// ---------------------------------------------------------------- codes

// ConnectCode is a new connection code (shown once).
type ConnectCode struct {
	Code      string
	ExpiresAt time.Time
}

// NewConnectCode makes a connection code for one of the account's saved
// instances.
func (s *Service) NewConnectCode(ctx context.Context, instanceID string, a pipeline.Actor) (ConnectCode, error) {
	if s.Data == nil {
		return ConnectCode{}, licensing.ErrDataKeyMissing
	}
	if s.Keys == nil || s.Keys.Index == nil {
		return ConnectCode{}, ErrIndexKeyMissing
	}
	if !uuidRe.MatchString(instanceID) {
		return ConnectCode{}, refuse(pipeline.NotFound, "instance_not_found", "No such saved instance.")
	}
	var out ConnectCode
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := store.InstanceOf(ctx, tx, a.AccountID, instanceID); errors.Is(err, store.ErrNotFound) {
			return refuse(pipeline.NotFound, "instance_not_found", "No such saved instance.")
		} else if err != nil {
			return err
		}
		if _, err := store.LockInstance(ctx, tx, instanceID); err != nil {
			return err
		}
		if err := store.DropExpiredConnectCodes(ctx, tx); err != nil {
			return err
		}
		n, err := store.CountLiveConnectCodes(ctx, tx, instanceID)
		if err != nil {
			return err
		}
		if n >= MaxLiveConnectCodes {
			return refuse(pipeline.Conflict, "connect_codes_full",
				"This instance has %d connection codes waiting; use one, or wait until they expire.", MaxLiveConnectCodes)
		}
		expires := s.now().Truncate(time.Second).Add(ConnectCodeTTL)
		for attempt := 0; ; attempt++ {
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				return err
			}
			code := ConnectCodePrefix + base64.RawURLEncoding.EncodeToString(b)
			err := store.InsertConnectCode(ctx, tx, tokenHash(code), instanceID, a.AccountID, expires)
			if errors.Is(err, store.ErrConflict) && attempt < 3 {
				continue
			}
			if err != nil {
				return err
			}
			out = ConnectCode{Code: code, ExpiresAt: expires}
			_, err = audit.Append(ctx, tx, s.entry(a, "instance.connect_code", "instance", instanceID,
				map[string]any{"expires_at": expires.UTC().Format(time.RFC3339)}))
			return err
		}
	})
	return out, err
}

// ---------------------------------------------------------------- connect

// ConnectRequest is what a filex sends with a code.
type ConnectRequest struct {
	Code            string
	PublicKey       string // 32 bytes, lower-case hex
	FilexOrigin     string // the filex's own address: it must be the instance's
	FilexInstanceID string // the filex's opaque id (license seats); optional
}

// Connect binds a filex's public key to the instance a code was made for, and
// answers the binding signed with the index key (domain.InstanceConnection):
// the filex checks the signature with the store's key it already trusts, and
// that the address is its own.
func (s *Service) Connect(ctx context.Context, in ConnectRequest, ip string) (domain.SignedEnvelope, error) {
	if s.Data == nil {
		return domain.SignedEnvelope{}, licensing.ErrDataKeyMissing
	}
	if s.Keys == nil || s.Keys.Index == nil {
		return domain.SignedEnvelope{}, ErrIndexKeyMissing
	}
	unknown := refuse(pipeline.NotFound, "not_found", "No such connection code.")
	if !connectCodeRe.MatchString(in.Code) {
		return domain.SignedEnvelope{}, unknown
	}
	key, err := hex.DecodeString(in.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize || strings.ToLower(in.PublicKey) != in.PublicKey {
		return domain.SignedEnvelope{}, refuse(pipeline.Invalid, "invalid_field", "public_key is an ed25519 public key: 64 lower-case hex characters.")
	}
	if in.FilexInstanceID != "" && !licensing.ValidInstanceID(in.FilexInstanceID) {
		return domain.SignedEnvelope{}, refuse(pipeline.Invalid, "invalid_field", "filex_instance_id must be 1 to 128 printable ASCII characters without spaces.")
	}
	claimed, err := NormalizeFilexURL(in.FilexOrigin, s.Development)
	if err != nil {
		return domain.SignedEnvelope{}, err
	}
	fpr := KeyFingerprint(key)
	var payload domain.InstanceConnection
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		c, err := store.LockConnectCode(ctx, tx, tokenHash(in.Code))
		if errors.Is(err, store.ErrNotFound) {
			return unknown
		}
		if err != nil {
			return err
		}
		if c.UsedAt != nil {
			return refuse(pipeline.Gone, "code_used", "This connection code was used already; make a new one on the store.")
		}
		if !s.now().Before(c.ExpiresAt) {
			return refuse(pipeline.Gone, "code_expired", "This connection code has expired; make a new one on the store.")
		}
		row, err := store.LockInstance(ctx, tx, c.InstanceID)
		if err != nil {
			return err
		}
		inst, err := s.open(row)
		if err != nil {
			return err
		}
		// ⚠ Before anything is bound: a code pasted into another filex than
		// the one it was made for binds nothing (that filex would refuse the
		// answer anyway, but its key would be left on this instance).
		if inst.URL != claimed {
			return refuse(pipeline.Conflict, "wrong_instance",
				"This connection code was made for another filex address; make one for this filex's own address.")
		}
		at, err := store.SetServerKey(ctx, tx, row.ID, key, fpr, in.FilexInstanceID)
		if errors.Is(err, store.ErrConflict) {
			return refuse(pipeline.Conflict, "key_in_use", "That key is connected to another instance; make a new key.")
		}
		if err != nil {
			return err
		}
		if err := store.SpendConnectCodes(ctx, tx, row.ID); err != nil {
			return err
		}
		payload = domain.InstanceConnection{Store: s.StoreOrigin, InstanceID: row.ID, FilexOrigin: inst.URL,
			KeyFingerprint: fpr, ConnectedAt: at.UTC().Format(index.TimeLayout)}
		owner := row.AccountID
		_, err = audit.Append(ctx, tx, audit.Entry{ActorAccountID: &owner, ActorRole: audit.RoleSystem, Action: "instance.connect",
			TargetType: "instance", TargetID: row.ID, IP: ip, Data: map[string]any{"key_fingerprint": fpr}})
		return err
	})
	if err != nil {
		return domain.SignedEnvelope{}, err
	}
	return signedjson.Sign(s.Keys.Index, payload)
}

// Disconnect clears an instance's key: by its owner on the store (actor), or
// by the connected filex itself (instance set, actor zero).
func (s *Service) Disconnect(ctx context.Context, instanceID string, a pipeline.Actor, byFilex bool) error {
	if !uuidRe.MatchString(instanceID) {
		return refuse(pipeline.NotFound, "instance_not_found", "No such saved instance.")
	}
	return db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		row, err := store.LockInstance(ctx, tx, instanceID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && !byFilex && row.AccountID != a.AccountID) {
			return refuse(pipeline.NotFound, "instance_not_found", "No such saved instance.")
		}
		if err != nil {
			return err
		}
		ok, err := store.ClearServerKey(ctx, tx, instanceID)
		if err != nil {
			return err
		}
		if !ok {
			return refuse(pipeline.Conflict, "not_connected", "No filex is connected to this instance.")
		}
		if err := store.SpendConnectCodes(ctx, tx, instanceID); err != nil {
			return err
		}
		e := s.entry(a, "instance.disconnect", "instance", instanceID, map[string]any{"by": "owner"})
		if byFilex {
			owner := row.AccountID
			e = audit.Entry{ActorAccountID: &owner, ActorRole: audit.RoleSystem, Action: "instance.disconnect",
				TargetType: "instance", TargetID: instanceID, IP: a.IP, Data: map[string]any{"by": "filex"}}
		}
		_, err = audit.Append(ctx, tx, e)
		return err
	})
}

// ---------------------------------------------------------------- intents

// InstanceIntentRequest is what a connected filex asks for.
type InstanceIntentRequest struct {
	App        string
	Version    string
	LicenseKey string
}

// CreateInstanceIntent makes an install intent for a connected instance's own
// filex: the address the instance was saved with is its filex_origin, its
// owner its account. Answers what the filex needs to read it.
func (s *Service) CreateInstanceIntent(ctx context.Context, inst store.SavedInstance, in InstanceIntentRequest, ip string) (Created, error) {
	if s.Data == nil {
		return Created{}, licensing.ErrDataKeyMissing
	}
	if s.Keys == nil || s.Keys.Index == nil {
		return Created{}, ErrIndexKeyMissing
	}
	pick, err := s.pick(ctx, in.App, in.Version, in.LicenseKey)
	if err != nil {
		return Created{}, err
	}
	if pick.canonKey != "" {
		// Only a license this instance holds a seat of, or can take one of:
		// a key is not a way to move another instance's seat.
		ok, err := s.seatFor(ctx, pick.lic, inst.FilexInstanceID)
		if err != nil {
			return Created{}, err
		}
		if !ok {
			return Created{}, refuse(pipeline.Invalid, "license_invalid",
				"That license key has no free seat for this filex; release one on the store first.")
		}
	}
	opened, err := s.open(inst)
	if err != nil {
		return Created{}, err
	}
	owner := inst.AccountID
	a := pipeline.Actor{AccountID: owner, Role: audit.RoleSystem, IP: ip}
	out := Created{App: pick.app.Name, Version: pick.ver.Version}
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		row, token, err := s.insertIntent(ctx, tx, owner, pick, opened.URL, store.IntentSourceInstance, inst.ID, a,
			map[string]any{"via": "instance", "instance": inst.ID})
		if err != nil {
			return err
		}
		out.Token, out.TokenID, out.ExpiresAt = token, row.ID, row.ExpiresAt
		return nil
	})
	if err != nil {
		return Created{}, err
	}
	return out, nil
}

// seatFor: the license has a seat for the filex whose opaque id is
// filexInstanceID - one it holds already, or a free one.
func (s *Service) seatFor(ctx context.Context, lic store.License, filexInstanceID string) (bool, error) {
	if filexInstanceID != "" {
		a, err := store.GetActivation(ctx, s.Pool, lic.ID, filexInstanceID)
		if err == nil && a.ReleasedAt == nil {
			return true, nil
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return false, err
		}
	}
	return lic.SeatsUsed < lic.Seats, nil
}

// InstanceView is a connected instance as its filex reads it about itself.
type InstanceView struct {
	ID             string
	FilexOrigin    string
	KeyFingerprint string
	ConnectedAt    time.Time
}

// Self answers what a connected filex reads about its own connection.
func (s *Service) Self(inst store.SavedInstance) (InstanceView, error) {
	opened, err := s.open(inst)
	if err != nil {
		return InstanceView{}, err
	}
	v := InstanceView{ID: inst.ID, FilexOrigin: opened.URL, KeyFingerprint: inst.ServerKeyFingerprint}
	if inst.ConnectedAt != nil {
		v.ConnectedAt = *inst.ConnectedAt
	}
	return v, nil
}
