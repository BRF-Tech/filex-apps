// Package install makes the store's install links (docs/ARCHITECTURE.md
// "Install links"): a signed-in person asks for an intent to install an
// approved version on their own filex and gets a one-time magic link to it;
// that filex's server reads the intent (GET /v1/install/{token}, signed with
// the index key) and reports what became of it. The person's filex
// instances can be saved, encrypted, for their owner only.
//
// The token is 32 random bytes; the database keeps its sha256. The link
// carries it in the URL fragment, so it reaches neither a server log nor a
// Referer. A license key the person adds is sealed until the intent is used
// or expires. A filex address is never logged or written to the audit log.
package install

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/audit"
	"github.com/brf-tech/filex-apps/backend/internal/datakey"
	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/licensing"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/signedjson"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// MaxInstances is how many filex instances one account may save.
const MaxInstances = 50

// Service holds the rules.
type Service struct {
	Pool *pgxpool.Pool
	Keys *index.Keyring // Index signs intents
	Data *datakey.Keys  // nil: FAPPS_DATA_KEY is not set, the feature is off
	// StoreOrigin is this store's origin (the payload's `store`, the link's
	// `store=`).
	StoreOrigin string
	// Development allows http://localhost and http://127.0.0.1 filex
	// addresses (APP_ENV=development).
	Development bool
	Log         *slog.Logger
	Now         func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func refuse(kind pipeline.RefusalKind, code, format string, args ...any) *pipeline.Refusal {
	return &pipeline.Refusal{Kind: kind, Code: code, Message: fmt.Sprintf(format, args...)}
}

// ErrIndexKeyMissing: no key to sign an intent with.
var ErrIndexKeyMissing = refuse(pipeline.Unavailable, "index_key_missing",
	"No index signing key is configured on this server: install links are off.")

var (
	appNameRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	tokenRe    = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	uuidRe     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	hostNameRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62})(?:\.[a-z0-9](?:[a-z0-9-]{0,62}))*$`)
)

// ---------------------------------------------------------------- addresses

var errURL = refuse(pipeline.Invalid, "filex_url_invalid",
	"Enter your filex's address with https and without a path, e.g. https://files.example.com.")

// NormalizeFilexURL reduces a filex address to its origin: https only (with
// development, http://localhost, http://127.0.0.1 and http://[::1] too), no
// user, no path, no query, no fragment; the host lower-cased, the default
// port dropped.
func NormalizeFilexURL(raw string, development bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 300 || strings.ContainsAny(raw, " \t\r\n\\") {
		return "", errURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" ||
		strings.HasSuffix(raw, "?") || strings.HasSuffix(raw, "#") || (u.Path != "" && u.Path != "/") {
		return "", errURL
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", errURL
		}
	}
	ipv6 := strings.Contains(host, ":")
	if !ipv6 && (!hostNameRe.MatchString(host) || len(host) > 253) {
		return "", errURL
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		if port == "443" {
			port = ""
		}
	case "http":
		if !development || (host != "localhost" && host != "127.0.0.1" && host != "::1") {
			return "", errURL
		}
		if port == "80" {
			port = ""
		}
	default:
		return "", errURL
	}
	hp := host
	if ipv6 {
		hp = "[" + host + "]"
	}
	if port != "" {
		hp += ":" + port
	}
	scheme := strings.ToLower(u.Scheme)
	return scheme + "://" + hp, nil
}

// EncodeURIComponent is JavaScript's encodeURIComponent: everything but
// A-Z a-z 0-9 - _ . ! ~ * ' ( ) percent-encoded as UTF-8.
func EncodeURIComponent(s string) string {
	const keep = "-_.!~*'()"
	var sb strings.Builder
	for _, b := range []byte(s) {
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.IndexByte(keep, b) >= 0 {
			sb.WriteByte(b)
			continue
		}
		fmt.Fprintf(&sb, "%%%02X", b)
	}
	return sb.String()
}

// MagicLink is the link a person opens on their own filex.
func MagicLink(filexOrigin, storeOrigin, token string) string {
	return filexOrigin + "/admin/store-install#store=" + EncodeURIComponent(storeOrigin) + "&intent=" + token
}

// ---------------------------------------------------------------- saved instances

// Instance is a saved filex instance as its owner sees it.
type Instance struct {
	ID        string
	Label     string
	URL       string
	CreatedAt time.Time
	// Connected: a filex server holds a key for it (connect.go); the key's
	// fingerprint, when it connected and when it last asked for something.
	Connected      bool
	KeyFingerprint string
	ConnectedAt    *time.Time
	LastRequestAt  *time.Time
}

func instanceAAD(id, field string) []byte { return []byte("saved_instances:" + id + ":" + field) }

func (s *Service) open(row store.SavedInstance) (Instance, error) {
	u, err := s.Data.OpenInstance(row.URLSealed, instanceAAD(row.ID, "url"))
	if err != nil {
		return Instance{}, err
	}
	l, err := s.Data.OpenInstance(row.LabelSealed, instanceAAD(row.ID, "label"))
	if err != nil {
		return Instance{}, err
	}
	in := Instance{ID: row.ID, Label: string(l), URL: string(u), CreatedAt: row.CreatedAt}
	if row.Connected() {
		in.Connected, in.KeyFingerprint, in.ConnectedAt, in.LastRequestAt = true, row.ServerKeyFingerprint, row.ConnectedAt, row.LastRequestAt
	}
	return in, nil
}

func label(v, origin string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		u, _ := url.Parse(origin)
		return u.Host, nil
	}
	if utf8.RuneCountInString(v) > 80 {
		return "", refuse(pipeline.Invalid, "invalid_field", "The label is at most 80 characters.")
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return "", refuse(pipeline.Invalid, "invalid_field", "The label holds a control character.")
		}
	}
	return v, nil
}

// Instances lists an account's saved instances.
func (s *Service) Instances(ctx context.Context, accountID int64) ([]Instance, error) {
	if s.Data == nil {
		return nil, licensing.ErrDataKeyMissing
	}
	rows, err := store.InstancesOf(ctx, s.Pool, accountID)
	if err != nil {
		return nil, err
	}
	out := []Instance{}
	for _, r := range rows {
		in, err := s.open(r)
		if err != nil {
			s.log().Warn("a saved instance does not open (FAPPS_DATA_KEY changed?)", "id", r.ID)
			continue
		}
		out = append(out, in)
	}
	return out, nil
}

// SaveInstance saves a filex address for an account; an address it saved
// before answers that instance (created false).
func (s *Service) SaveInstance(ctx context.Context, rawURL, rawLabel string, a pipeline.Actor) (Instance, bool, error) {
	if s.Data == nil {
		return Instance{}, false, licensing.ErrDataKeyMissing
	}
	origin, err := NormalizeFilexURL(rawURL, s.Development)
	if err != nil {
		return Instance{}, false, err
	}
	lbl, err := label(rawLabel, origin)
	if err != nil {
		return Instance{}, false, err
	}
	var out Instance
	var created bool
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		out, created, err = s.save(ctx, tx, origin, lbl, a)
		return err
	})
	return out, created, err
}

func (s *Service) save(ctx context.Context, tx pgx.Tx, origin, lbl string, a pipeline.Actor) (Instance, bool, error) {
	idx := s.Data.InstanceIndex(a.AccountID, origin)
	if row, err := store.InstanceByIndex(ctx, tx, a.AccountID, idx); err == nil {
		in, err := s.open(row)
		return in, false, err
	} else if !errors.Is(err, store.ErrNotFound) {
		return Instance{}, false, err
	}
	n, err := store.CountInstances(ctx, tx, a.AccountID)
	if err != nil {
		return Instance{}, false, err
	}
	if n >= MaxInstances {
		return Instance{}, false, refuse(pipeline.Conflict, "instances_full", "At most %d filex instances can be saved; forget one first.", MaxInstances)
	}
	id, err := store.NewInstanceID(ctx, tx)
	if err != nil {
		return Instance{}, false, err
	}
	us, err := s.Data.SealInstance([]byte(origin), instanceAAD(id, "url"))
	if err != nil {
		return Instance{}, false, err
	}
	ls, err := s.Data.SealInstance([]byte(lbl), instanceAAD(id, "label"))
	if err != nil {
		return Instance{}, false, err
	}
	if err := store.InsertInstance(ctx, tx, store.SavedInstance{ID: id, AccountID: a.AccountID, URLIndex: idx, URLSealed: us, LabelSealed: ls}); err != nil {
		return Instance{}, false, err
	}
	if _, err := audit.Append(ctx, tx, s.entry(a, "instance.save", "instance", id, map[string]any{})); err != nil {
		return Instance{}, false, err
	}
	row, err := store.InstanceOf(ctx, tx, a.AccountID, id)
	if err != nil {
		return Instance{}, false, err
	}
	in, err := s.open(row)
	return in, true, err
}

// DeleteInstance forgets one of the account's saved instances.
func (s *Service) DeleteInstance(ctx context.Context, id string, a pipeline.Actor) error {
	if !uuidRe.MatchString(id) {
		return refuse(pipeline.NotFound, "not_found", "No such saved instance.")
	}
	return db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		ok, err := store.DeleteInstance(ctx, tx, a.AccountID, id)
		if err != nil {
			return err
		}
		if !ok {
			return refuse(pipeline.NotFound, "not_found", "No such saved instance.")
		}
		_, err = audit.Append(ctx, tx, s.entry(a, "instance.delete", "instance", id, map[string]any{}))
		return err
	})
}

func (s *Service) entry(a pipeline.Actor, action, targetType, targetID string, data map[string]any) audit.Entry {
	var actor *int64
	if a.AccountID != 0 {
		id := a.AccountID
		actor = &id
	}
	role := a.Role
	if role == "" {
		role = audit.RoleAnonymous
	}
	return audit.Entry{ActorAccountID: actor, ActorRole: role, Action: action, TargetType: targetType,
		TargetID: targetID, IP: a.IP, Data: data}
}

// ---------------------------------------------------------------- intents

// IntentRequest is what a person asks for.
type IntentRequest struct {
	App           string
	Version       string
	FilexURL      string
	InstanceID    string // a saved instance, instead of FilexURL
	SaveInstance  bool
	InstanceLabel string
	LicenseKey    string
}

// Created is a new install link.
type Created struct {
	Token     string
	TokenID   string // the intent's short id (the payload's token_id)
	MagicLink string
	ExpiresAt time.Time
	App       string
	Version   string
	Instance  *Instance // the saved instance (save_instance, or the one asked for)
}

func tokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func intentAAD(id string) []byte { return []byte("install_intents:" + id + ":license_key") }

func originAAD(id string) []byte { return []byte("install_intents:" + id + ":filex_origin") }

// pickVersion: the version asked for, or the newest that is not yanked.
func pickVersion(versions []store.Version, want string) (store.Version, error) {
	if want != "" {
		for _, v := range versions {
			if v.Version == want {
				if v.YankedAt != nil {
					return v, refuse(pipeline.Conflict, "version_yanked", "Version %s is yanked; it is not installed any more.", want)
				}
				return v, nil
			}
		}
		return store.Version{}, refuse(pipeline.NotFound, "version_not_found", "The app has no version %q.", want)
	}
	latest := index.Latest(versions)
	for _, v := range versions {
		if v.Version == latest && v.YankedAt == nil {
			return v, nil
		}
	}
	return store.Version{}, refuse(pipeline.Conflict, "no_installable_version", "Every version of this app is yanked.")
}

// CreateIntent makes an install link.
func (s *Service) CreateIntent(ctx context.Context, in IntentRequest, a pipeline.Actor) (Created, error) {
	if s.Data == nil {
		return Created{}, licensing.ErrDataKeyMissing
	}
	if s.Keys == nil || s.Keys.Index == nil {
		return Created{}, ErrIndexKeyMissing
	}
	if (in.FilexURL == "") == (in.InstanceID == "") {
		return Created{}, refuse(pipeline.Invalid, "invalid_field", "Give either filex_url or instance_id.")
	}
	if in.SaveInstance && in.FilexURL == "" {
		return Created{}, refuse(pipeline.Invalid, "invalid_field", "save_instance goes with filex_url.")
	}
	pick, err := s.pick(ctx, in.App, in.Version, in.LicenseKey)
	if err != nil {
		return Created{}, err
	}

	var origin string
	var saved *Instance
	if in.FilexURL != "" {
		if origin, err = NormalizeFilexURL(in.FilexURL, s.Development); err != nil {
			return Created{}, err
		}
	} else {
		if !uuidRe.MatchString(in.InstanceID) {
			return Created{}, refuse(pipeline.NotFound, "instance_not_found", "No such saved instance.")
		}
		row, err := store.InstanceOf(ctx, s.Pool, a.AccountID, in.InstanceID)
		if errors.Is(err, store.ErrNotFound) {
			return Created{}, refuse(pipeline.NotFound, "instance_not_found", "No such saved instance.")
		}
		if err != nil {
			return Created{}, err
		}
		inst, err := s.open(row)
		if err != nil {
			return Created{}, err
		}
		origin, saved = inst.URL, &inst
	}
	lbl := ""
	if in.SaveInstance {
		if lbl, err = label(in.InstanceLabel, origin); err != nil {
			return Created{}, err
		}
	}

	out := Created{App: pick.app.Name, Version: pick.ver.Version, Instance: saved}
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if in.SaveInstance {
			inst, _, err := s.save(ctx, tx, origin, lbl, a)
			if err != nil {
				return err
			}
			out.Instance = &inst
		}
		data := map[string]any{"saved_instance": in.SaveInstance || in.InstanceID != ""}
		row, token, err := s.insertIntent(ctx, tx, a.AccountID, pick, origin, store.IntentSourceWeb, "", a, data)
		if err != nil {
			return err
		}
		out.Token, out.ExpiresAt, out.TokenID = token, row.ExpiresAt, row.ID
		return nil
	})
	if err != nil {
		return Created{}, err
	}
	out.MagicLink = MagicLink(origin, s.StoreOrigin, out.Token)
	return out, nil
}

// picked is what an intent will name: the app, its version, and the
// license whose key the intent carries (canonKey "" when none).
type picked struct {
	app      store.App
	ver      store.Version
	lic      store.License
	canonKey string
}

// pick checks what an intent is asked for: a listed app, a version of it that
// is not yanked (the newest when none is named), and a license key that is an
// active key of that app (one code for every reason it is not).
func (s *Service) pick(ctx context.Context, appName, version, licenseKey string) (picked, error) {
	if len(version) > 64 {
		return picked{}, refuse(pipeline.NotFound, "version_not_found", "No such version.")
	}
	if !appNameRe.MatchString(appName) {
		return picked{}, refuse(pipeline.NotFound, "not_found", "No such app.")
	}
	app, err := store.GetAppByName(ctx, s.Pool, appName)
	if errors.Is(err, store.ErrNotFound) {
		return picked{}, refuse(pipeline.NotFound, "not_found", "No such app.")
	}
	if err != nil {
		return picked{}, err
	}
	if app.Status == "revoked" {
		return picked{}, refuse(pipeline.Conflict, "app_revoked", "The app is revoked; it is not installed any more.")
	}
	versions, err := store.VersionsOf(ctx, s.Pool, app.ID)
	if err != nil {
		return picked{}, err
	}
	ver, err := pickVersion(versions, strings.TrimSpace(version))
	if err != nil {
		return picked{}, err
	}
	out := picked{app: app, ver: ver}
	if strings.TrimSpace(licenseKey) != "" {
		badKey := refuse(pipeline.Invalid, "license_invalid", "That license key is not an active key of %s.", app.Name)
		k, ok := licensing.NormalizeKey(licenseKey)
		if !ok {
			return picked{}, badKey
		}
		lic, err := store.LicenseByHash(ctx, s.Pool, s.Data.LicenseKeyHash(k))
		if errors.Is(err, store.ErrNotFound) {
			return picked{}, badKey
		}
		if err != nil {
			return picked{}, err
		}
		if lic.AppID != app.ID || lic.Status != "active" || (lic.ValidUntil != "" && s.now().Format(index.DateLayout) > lic.ValidUntil) {
			return picked{}, badKey
		}
		out.lic, out.canonKey = lic, k
	}
	return out, nil
}

// insertIntent stores a new intent for origin inside tx and answers the row
// and the token (shown once). data is added to the audit row.
func (s *Service) insertIntent(ctx context.Context, tx pgx.Tx, accountID int64, p picked, origin, source, instanceID string,
	a pipeline.Actor, data map[string]any) (store.InstallIntent, string, error) {
	if _, err := store.DropExpiredIntentKeys(ctx, tx); err != nil {
		return store.InstallIntent{}, "", err
	}
	expires := s.now().Truncate(time.Second).Add(domain.InstallIntentTTL)
	for attempt := 0; ; attempt++ {
		idb := make([]byte, 8)
		tb := make([]byte, 32)
		if _, err := rand.Read(idb); err != nil {
			return store.InstallIntent{}, "", err
		}
		if _, err := rand.Read(tb); err != nil {
			return store.InstallIntent{}, "", err
		}
		row := store.InstallIntent{ID: hex.EncodeToString(idb), AccountID: accountID, AppID: p.app.ID,
			VersionID: p.ver.ID, ExpiresAt: expires, Source: source, RequestedByInstance: instanceID}
		// The payload names the filex the link is for (filex_origin);
		// kept sealed like a saved instance's address.
		sealedOrigin, serr := s.Data.SealIntent([]byte(origin), originAAD(row.ID))
		if serr != nil {
			return store.InstallIntent{}, "", serr
		}
		row.FilexOriginSealed = sealedOrigin
		if p.canonKey != "" {
			sealed, err := s.Data.SealIntent([]byte(p.canonKey), intentAAD(row.ID))
			if err != nil {
				return store.InstallIntent{}, "", err
			}
			row.LicenseID, row.LicenseKeySealed = p.lic.ID, sealed
		}
		token := base64.RawURLEncoding.EncodeToString(tb)
		err := store.InsertIntent(ctx, tx, row, tokenHash(token))
		if errors.Is(err, store.ErrConflict) && attempt < 3 {
			continue
		}
		if err != nil {
			return store.InstallIntent{}, "", err
		}
		entry := map[string]any{"app": p.app.Name, "version": p.ver.Version, "token_id": row.ID}
		for k, v := range data {
			entry[k] = v
		}
		if p.canonKey != "" {
			entry["license"] = p.lic.ID
			entry["key_prefix"] = p.lic.KeyPrefix
		}
		if _, err := audit.Append(ctx, tx, s.entry(a, "install_intent.create", "install_intent", row.ID, entry)); err != nil {
			return store.InstallIntent{}, "", err
		}
		return row, token, nil
	}
}

// intentOf finds a live intent by its token: 404 for a token the store
// never made, 410 for one used or expired.
func (s *Service) intentOf(ctx context.Context, q db.Querier, token string, lock bool) (store.InstallIntent, error) {
	notFound := refuse(pipeline.NotFound, "not_found", "No such install link.")
	if !tokenRe.MatchString(token) {
		return store.InstallIntent{}, notFound
	}
	var it store.InstallIntent
	var err error
	if lock {
		it, err = store.LockIntentByTokenHash(ctx, q, tokenHash(token))
	} else {
		it, err = store.IntentByTokenHash(ctx, q, tokenHash(token))
	}
	if errors.Is(err, store.ErrNotFound) {
		return it, notFound
	}
	if err != nil {
		return it, err
	}
	if it.CompletedAt != nil {
		return it, refuse(pipeline.Gone, "intent_used", "This install link was used already.")
	}
	if !s.now().Before(it.ExpiresAt) {
		return it, refuse(pipeline.Gone, "intent_expired", "This install link has expired; make a new one on the store.")
	}
	return it, nil
}

// ReadIntent is what a filex reads: the intent's payload, signed with the
// index key.
func (s *Service) ReadIntent(ctx context.Context, token string) (domain.SignedEnvelope, error) {
	if s.Keys == nil || s.Keys.Index == nil {
		return domain.SignedEnvelope{}, ErrIndexKeyMissing
	}
	it, err := s.intentOf(ctx, s.Pool, token, false)
	if err != nil {
		return domain.SignedEnvelope{}, err
	}
	app, err := store.GetAppByID(ctx, s.Pool, it.AppID)
	if err != nil {
		return domain.SignedEnvelope{}, err
	}
	ver, err := store.VersionByID(ctx, s.Pool, it.VersionID)
	if err != nil {
		return domain.SignedEnvelope{}, err
	}
	if app.Status == "revoked" {
		return domain.SignedEnvelope{}, refuse(pipeline.Conflict, "app_revoked", "The app was revoked after this link was made.")
	}
	if ver.YankedAt != nil {
		return domain.SignedEnvelope{}, refuse(pipeline.Conflict, "version_yanked", "Version %s was yanked after this link was made.", ver.Version)
	}
	// filex requires commit and manifest_sha256; the database keeps both
	// for every approved version, so an empty one is a broken row and is
	// never signed.
	if ver.Commit == "" || ver.ManifestSHA256 == "" {
		return domain.SignedEnvelope{}, fmt.Errorf("version %d has no commit or manifest sha256", ver.ID)
	}
	if s.Data == nil {
		return domain.SignedEnvelope{}, licensing.ErrDataKeyMissing
	}
	origin, err := s.Data.OpenIntent(it.FilexOriginSealed, originAAD(it.ID))
	if err != nil {
		return domain.SignedEnvelope{}, err
	}
	p := domain.InstallIntentPayload{
		Store: s.StoreOrigin, FilexOrigin: string(origin), TokenID: it.ID, App: app.Name, Kind: domain.AppKind(app.Kind), Version: ver.Version,
		Repo: app.Repo(), Ref: ver.Ref, Commit: ver.Commit, ManifestSHA256: ver.ManifestSHA256,
		Permissions: append([]string{}, ver.Permissions...), FilexRange: ver.FilexRange,
		Paid: app.PricingModel == string(domain.PricingPaid), ExpiresAt: it.ExpiresAt.UTC().Format(index.TimeLayout),
	}
	if ver.Wasm != nil {
		p.WasmSHA256 = ver.Wasm.SHA256
	}
	if ver.UI != nil {
		p.UISHA256 = ver.UI.SHA256
	}
	if app.Kind == string(domain.KindStorage) {
		// A storage plugin's intent: the release's feed it was listed from
		// (its sha256 is manifest_sha256), every build with the artifact
		// key's signature, and the store's conformance run. filex installs
		// the build for its own platform "from its source", held to these.
		if len(ver.Binaries) == 0 {
			return domain.SignedEnvelope{}, fmt.Errorf("storage plugin version %d has no builds", ver.ID)
		}
		p.FeedURL = ver.ManifestURL
		p.Binaries = map[string]domain.IntentBinary{}
		for plat, b := range ver.Binaries {
			p.Binaries[plat] = domain.IntentBinary{URL: b.URL, SHA256: b.SHA256, Size: b.Size, Sig: b.Sig}
		}
		if ver.Conformance != nil {
			c := *ver.Conformance
			c.Capabilities = append([]string{}, c.Capabilities...)
			p.Conformance = &c
		}
		p.Permissions = []string{}
	}
	if len(it.LicenseKeySealed) > 0 {
		k, err := s.Data.OpenIntent(it.LicenseKeySealed, intentAAD(it.ID))
		if err != nil {
			return domain.SignedEnvelope{}, err
		}
		p.LicenseKey = string(k)
	}
	return signedjson.Sign(s.Keys.Index, p)
}

// Completed is what a completion recorded.
type Completed struct {
	TokenID     string
	Result      domain.InstallResult
	CompletedAt time.Time
}

// CompleteIntent spends a token with the filex's outcome.
func (s *Service) CompleteIntent(ctx context.Context, token, instanceID string, result domain.InstallResult, ip string) (Completed, error) {
	if !licensing.ValidInstanceID(instanceID) {
		return Completed{}, refuse(pipeline.Invalid, "invalid_field", "instance_id must be 1 to 128 printable ASCII characters without spaces.")
	}
	if result != domain.InstallInstalled && result != domain.InstallCancelled {
		return Completed{}, refuse(pipeline.Invalid, "invalid_field", "result must be installed or cancelled.")
	}
	var out Completed
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		it, err := s.intentOf(ctx, tx, token, true)
		if err != nil {
			return err
		}
		at, ok, err := store.CompleteIntent(ctx, tx, it.ID, string(result), instanceID)
		if err != nil {
			return err
		}
		if !ok {
			return refuse(pipeline.Gone, "intent_used", "This install link was used already.")
		}
		out = Completed{TokenID: it.ID, Result: result, CompletedAt: at}
		_, err = audit.Append(ctx, tx, s.entry(pipeline.Actor{Role: audit.RoleAnonymous, IP: ip}, "install_intent.complete",
			"install_intent", it.ID, map[string]any{"result": string(result), "instance_id": instanceID}))
		return err
	})
	return out, err
}
