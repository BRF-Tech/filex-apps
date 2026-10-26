// Package licensing is the store's side of paid apps (docs/ARCHITECTURE.md
// "Licensing"): an app's price (display only, the store takes no payment),
// the license keys a publisher member or an administrator generates, and
// the public check a filex server makes (POST /v1/licenses/verify), whose
// verdict is signed with the store's license key.
//
// A key exists in the clear only in the answer that created it and in the
// requests that carry it: the database keeps its HMAC (internal/datakey,
// "license-key") and its first 8 characters; logs and the audit log name a
// license by its id and prefix.
package licensing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
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
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/signedjson"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// Service holds the rules.
type Service struct {
	Pool *pgxpool.Pool
	Keys *index.Keyring // License signs verdicts
	Data *datakey.Keys  // nil: FAPPS_DATA_KEY is not set, licenses are off
	Log  *slog.Logger
	Now  func() time.Time
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

// ErrDataKeyMissing and ErrLicenseKeyMissing say a feature is off.
var (
	ErrDataKeyMissing = refuse(pipeline.Unavailable, "data_key_missing",
		"FAPPS_DATA_KEY is not set on this server: licenses, install links and saved instances are off.")
	ErrLicenseKeyMissing = refuse(pipeline.Unavailable, "license_key_missing",
		"No license signing key is configured on this server (FAPPS_LICENSE_KEY): license checks are off.")
)

var (
	appNameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	instanceIDRe = regexp.MustCompile(`^[!-~]{1,128}$`)
	uuidRe       = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	dateRe       = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	emailRe      = regexp.MustCompile(`^[^\s@]{1,64}@[^\s@]{1,190}\.[^\s@]{1,63}$`)
	loginRe      = regexp.MustCompile(`^@?[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
)

// ValidInstanceID is filex's opaque instance id: printable ASCII, no space,
// 1 to 128 characters.
func ValidInstanceID(s string) bool { return instanceIDRe.MatchString(s) }

// ValidID is a license id (a uuid, lower case).
func ValidID(s string) bool { return uuidRe.MatchString(s) }

// ---------------------------------------------------------------- authorization

// manage: an administrator, or a member of the app's publisher while it is
// active.
func (s *Service) manage(ctx context.Context, q db.Querier, app store.App, a pipeline.Actor) error {
	if a.Admin {
		return nil
	}
	role, err := store.MemberRole(ctx, q, app.PublisherID, a.AccountID)
	if err != nil {
		return err
	}
	if role == "" {
		return refuse(pipeline.Forbidden, "not_a_member", "Only a member of the publisher %q or an administrator manages this app's licenses.", app.PublisherID)
	}
	pub, err := store.GetPublisher(ctx, q, app.PublisherID)
	if err != nil {
		return err
	}
	if pub.Status != "active" {
		return refuse(pipeline.Forbidden, "publisher_not_active", "The publisher %q is %s.", pub.ID, pub.Status)
	}
	return nil
}

func (s *Service) appByName(ctx context.Context, q db.Querier, name string, lock bool) (store.App, error) {
	if !appNameRe.MatchString(name) {
		return store.App{}, refuse(pipeline.NotFound, "not_found", "No such app.")
	}
	var app store.App
	var err error
	if lock {
		app, err = store.LockApp(ctx, q, name)
	} else {
		app, err = store.GetAppByName(ctx, q, name)
	}
	if errors.Is(err, store.ErrNotFound) {
		return app, refuse(pipeline.NotFound, "not_found", "No such app.")
	}
	return app, err
}

// CanManage reports whether the actor manages the app's price and licenses
// (the web interface shows the controls by it).
func (s *Service) CanManage(ctx context.Context, name string, a pipeline.Actor) (bool, error) {
	app, err := s.appByName(ctx, s.Pool, name, false)
	if err != nil {
		return false, err
	}
	err = s.manage(ctx, s.Pool, app, a)
	var ref *pipeline.Refusal
	if errors.As(err, &ref) {
		return false, nil
	}
	return err == nil, err
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

// ---------------------------------------------------------------- text rules

// Text that may reach a signed payload: no control character, no U+2028 or
// U+2029 (signedjson refuses them), trimmed, within bounds.
func payloadText(field, v string, min, max int) (string, error) {
	v = strings.TrimSpace(v)
	n := utf8.RuneCountInString(v)
	if n < min || n > max {
		if n == 0 {
			return "", refuse(pipeline.Invalid, "invalid_field", "%s is required.", field)
		}
		return "", refuse(pipeline.Invalid, "invalid_field", "%s must be %d to %d characters.", field, min, max)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f || r == ' ' || r == ' ' {
			return "", refuse(pipeline.Invalid, "invalid_field", "%s holds a control character.", field)
		}
	}
	return v, nil
}

func noteText(v string) (string, error) {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) > 2000 {
		return "", refuse(pipeline.Invalid, "invalid_field", "note is at most 2000 characters.")
	}
	for _, r := range v {
		if (r < 0x20 && r != '\n' && r != '\t' && r != '\r') || r == 0x7f {
			return "", refuse(pipeline.Invalid, "invalid_field", "note holds a control character.")
		}
	}
	return v, nil
}

// futureDate reads YYYY-MM-DD, today (UTC) or later; "" stays "".
func (s *Service) futureDate(field, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	d, err := time.Parse(index.DateLayout, v)
	if !dateRe.MatchString(v) || err != nil {
		return "", refuse(pipeline.Invalid, "invalid_field", "%s must be a date like 2027-12-31.", field)
	}
	today := s.now().Format(index.DateLayout)
	if d.Format(index.DateLayout) < today {
		return "", refuse(pipeline.Invalid, "invalid_field", "%s is in the past.", field)
	}
	return d.Format(index.DateLayout), nil
}

// ---------------------------------------------------------------- pricing

// PricingOf is an app's price as the API writes it.
func PricingOf(a store.App) domain.Pricing {
	if a.PricingModel == string(domain.PricingPaid) {
		return domain.Pricing{Model: domain.PricingPaid, PriceText: a.PriceText, PurchaseURL: a.PurchaseURL}
	}
	return domain.Pricing{Model: domain.PricingFree}
}

// CheckPricing validates a price: a free app has no price text and no
// purchase address; a paid app's address is https.
func CheckPricing(p domain.Pricing) (domain.Pricing, error) {
	switch p.Model {
	case domain.PricingFree:
		if strings.TrimSpace(p.PriceText) != "" || strings.TrimSpace(p.PurchaseURL) != "" {
			return p, refuse(pipeline.Invalid, "invalid_field", "A free app has no price_text and no purchase_url.")
		}
		return domain.Pricing{Model: domain.PricingFree}, nil
	case domain.PricingPaid:
	default:
		return p, refuse(pipeline.Invalid, "invalid_field", "model must be free or paid.")
	}
	out := domain.Pricing{Model: domain.PricingPaid}
	if strings.TrimSpace(p.PriceText) != "" {
		t, err := payloadText("price_text", p.PriceText, 1, 80)
		if err != nil {
			return p, err
		}
		out.PriceText = t
	}
	if v := strings.TrimSpace(p.PurchaseURL); v != "" {
		u, err := url.Parse(v)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(v) > 500 ||
			strings.ContainsAny(v, " \t\r\n\\") {
			return p, refuse(pipeline.Invalid, "invalid_field", "purchase_url must be an https address (at most 500 characters).")
		}
		out.PurchaseURL = v
	}
	return out, nil
}

// SetPricing sets an app's price.
func (s *Service) SetPricing(ctx context.Context, name string, p domain.Pricing, a pipeline.Actor) (domain.Pricing, error) {
	clean, err := CheckPricing(p)
	if err != nil {
		return domain.Pricing{}, err
	}
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		app, err := s.appByName(ctx, tx, name, true)
		if err != nil {
			return err
		}
		if err := s.manage(ctx, tx, app, a); err != nil {
			return err
		}
		if err := store.SetAppPricing(ctx, tx, app.ID, string(clean.Model), clean.PriceText, clean.PurchaseURL); err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, s.entry(a, "app.pricing", "app", app.Name,
			map[string]any{"from": PricingOf(app), "to": clean}))
		return err
	})
	return clean, err
}

// ---------------------------------------------------------------- licenses

// CreateInput is a new license.
type CreateInput struct {
	LicenseeName    string
	LicenseeContact string
	Seats           int // 0: 1
	ValidUntil      string
	UpdatesUntil    string
	Note            string
}

// Create generates a license key for an app. The key is returned here and
// nowhere else, ever.
func (s *Service) Create(ctx context.Context, name string, in CreateInput, a pipeline.Actor) (store.License, string, error) {
	if s.Data == nil {
		return store.License{}, "", ErrDataKeyMissing
	}
	licensee, err := payloadText("licensee_name", in.LicenseeName, 1, 120)
	if err != nil {
		return store.License{}, "", err
	}
	contact := strings.TrimSpace(in.LicenseeContact)
	if contact != "" && (utf8.RuneCountInString(contact) > 200 || !(emailRe.MatchString(contact) || loginRe.MatchString(contact))) {
		return store.License{}, "", refuse(pipeline.Invalid, "invalid_field", "licensee_contact must be an e-mail address or a GitHub login.")
	}
	seats := in.Seats
	if seats == 0 {
		seats = 1
	}
	if seats < 1 || seats > 100000 {
		return store.License{}, "", refuse(pipeline.Invalid, "invalid_field", "seats must be 1 to 100000.")
	}
	validUntil, err := s.futureDate("valid_until", in.ValidUntil)
	if err != nil {
		return store.License{}, "", err
	}
	updatesUntil, err := s.futureDate("updates_until", in.UpdatesUntil)
	if err != nil {
		return store.License{}, "", err
	}
	note, err := noteText(in.Note)
	if err != nil {
		return store.License{}, "", err
	}

	var lic store.License
	var key string
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		app, err := s.appByName(ctx, tx, name, false)
		if err != nil {
			return err
		}
		if err := s.manage(ctx, tx, app, a); err != nil {
			return err
		}
		if app.Status == "revoked" {
			return refuse(pipeline.Conflict, "app_revoked", "The app is revoked; it takes no new licenses.")
		}
		by := a.AccountID
		row := store.License{AppID: app.ID, LicenseeName: licensee, LicenseeContact: contact, Seats: seats,
			ValidUntil: validUntil, UpdatesUntil: updatesUntil, Note: note}
		if by != 0 {
			row.CreatedBy = &by
		}
		for attempt := 0; ; attempt++ {
			key = NewKey()
			row.KeyPrefix = Prefix(key)
			lic, err = store.InsertLicense(ctx, tx, row, s.Data.LicenseKeyHash(key))
			if errors.Is(err, store.ErrConflict) && attempt < 3 {
				continue
			}
			if err != nil {
				return err
			}
			break
		}
		_, err = audit.Append(ctx, tx, s.entry(a, "license.create", "license", lic.ID, map[string]any{
			"app": app.Name, "key_prefix": lic.KeyPrefix, "licensee": licensee, "seats": seats,
			"valid_until": validUntil, "updates_until": updatesUntil,
		}))
		return err
	})
	if err != nil {
		return store.License{}, "", err
	}
	return lic, key, nil
}

// List lists an app's licenses.
func (s *Service) List(ctx context.Context, name string, page store.Page, a pipeline.Actor) ([]store.License, int, error) {
	if s.Data == nil {
		return nil, 0, ErrDataKeyMissing
	}
	app, err := s.appByName(ctx, s.Pool, name, false)
	if err != nil {
		return nil, 0, err
	}
	if err := s.manage(ctx, s.Pool, app, a); err != nil {
		return nil, 0, err
	}
	return store.ListLicenses(ctx, s.Pool, app.ID, page)
}

func (s *Service) licenseFor(ctx context.Context, q db.Querier, id string, lock bool, a pipeline.Actor) (store.License, store.App, error) {
	if !ValidID(id) {
		return store.License{}, store.App{}, refuse(pipeline.NotFound, "not_found", "No such license.")
	}
	var lic store.License
	var err error
	if lock {
		lic, err = store.LockLicense(ctx, q, id)
	} else {
		lic, err = store.GetLicense(ctx, q, id)
	}
	if errors.Is(err, store.ErrNotFound) {
		return lic, store.App{}, refuse(pipeline.NotFound, "not_found", "No such license.")
	}
	if err != nil {
		return lic, store.App{}, err
	}
	app, err := store.GetAppByID(ctx, q, lic.AppID)
	if err != nil {
		return lic, app, err
	}
	if err := s.manage(ctx, q, app, a); err != nil {
		return lic, app, err
	}
	return lic, app, nil
}

// Revoke revokes a license for good.
func (s *Service) Revoke(ctx context.Context, id, reason string, a pipeline.Actor) (store.License, error) {
	reason, err := noteText(reason)
	if err != nil {
		return store.License{}, err
	}
	if utf8.RuneCountInString(reason) > 500 {
		return store.License{}, refuse(pipeline.Invalid, "invalid_field", "reason is at most 500 characters.")
	}
	var out store.License
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		lic, app, err := s.licenseFor(ctx, tx, id, true, a)
		if err != nil {
			return err
		}
		ok, err := store.RevokeLicense(ctx, tx, lic.ID, a.AccountID)
		if err != nil {
			return err
		}
		if !ok {
			return refuse(pipeline.Conflict, "already_revoked", "The license is already revoked.")
		}
		if _, err := audit.Append(ctx, tx, s.entry(a, "license.revoke", "license", lic.ID, map[string]any{
			"app": app.Name, "key_prefix": lic.KeyPrefix, "reason": reason,
		})); err != nil {
			return err
		}
		out, err = store.GetLicense(ctx, tx, lic.ID)
		return err
	})
	return out, err
}

// Activations lists the instances that use a license.
func (s *Service) Activations(ctx context.Context, id string, a pipeline.Actor) ([]store.Activation, error) {
	lic, _, err := s.licenseFor(ctx, s.Pool, id, false, a)
	if err != nil {
		return nil, err
	}
	return store.Activations(ctx, s.Pool, lic.ID)
}

// Release frees an instance's seat.
func (s *Service) Release(ctx context.Context, id, instanceID string, a pipeline.Actor) (store.Activation, error) {
	if !ValidInstanceID(instanceID) {
		return store.Activation{}, refuse(pipeline.NotFound, "not_found", "No such activation.")
	}
	var out store.Activation
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		lic, app, err := s.licenseFor(ctx, tx, id, true, a)
		if err != nil {
			return err
		}
		if _, err := store.GetActivation(ctx, tx, lic.ID, instanceID); errors.Is(err, store.ErrNotFound) {
			return refuse(pipeline.NotFound, "not_found", "No such activation.")
		} else if err != nil {
			return err
		}
		ok, err := store.ReleaseActivation(ctx, tx, lic.ID, instanceID)
		if err != nil {
			return err
		}
		if !ok {
			return refuse(pipeline.Conflict, "not_active", "The activation is already released.")
		}
		if _, err := audit.Append(ctx, tx, s.entry(a, "license.release", "license", lic.ID, map[string]any{
			"app": app.Name, "key_prefix": lic.KeyPrefix, "instance_id": instanceID,
		})); err != nil {
			return err
		}
		out, err = store.GetActivation(ctx, tx, lic.ID, instanceID)
		return err
	})
	return out, err
}

// ---------------------------------------------------------------- the check

// VerifyRequest is a filex server's license check.
type VerifyRequest struct {
	Key          string
	App          string
	InstanceID   string
	FilexVersion string
}

// KeySubject is what the per-key rate limit counts a check under: a short
// piece of the key's HMAC, never the key.
func (s *Service) KeySubject(key string) string {
	canon, ok := NormalizeKey(key)
	if !ok || s.Data == nil {
		return "malformed"
	}
	return fmt.Sprintf("%x", s.Data.LicenseKeyHash(canon)[:12])
}

// Verify checks a key for an app on one instance and signs the verdict.
// Order: unknown key (invalid), another app's key (wrong_app), revoked,
// expired, seats (seats_exhausted), else valid - which records the
// instance's activation.
func (s *Service) Verify(ctx context.Context, req VerifyRequest, ip string) (domain.SignedEnvelope, domain.LicenseVerdict, error) {
	if s.Data == nil {
		return domain.SignedEnvelope{}, domain.LicenseVerdict{}, ErrDataKeyMissing
	}
	if s.Keys == nil || s.Keys.License == nil {
		return domain.SignedEnvelope{}, domain.LicenseVerdict{}, ErrLicenseKeyMissing
	}
	if !appNameRe.MatchString(req.App) {
		return domain.SignedEnvelope{}, domain.LicenseVerdict{}, refuse(pipeline.Invalid, "invalid_field", "app is not an app name.")
	}
	if !ValidInstanceID(req.InstanceID) {
		return domain.SignedEnvelope{}, domain.LicenseVerdict{}, refuse(pipeline.Invalid, "invalid_field", "instance_id must be 1 to 128 printable ASCII characters without spaces.")
	}
	filexVersion := cleanVersion(req.FilexVersion)
	now := s.now().Truncate(time.Second)
	v := domain.LicenseVerdict{
		Result: domain.LicenseInvalid, App: req.App, InstanceID: req.InstanceID,
		CheckedAt:   now.Format(index.TimeLayout),
		NextCheckBy: now.Add(domain.LicenseCheckEvery).Format(index.TimeLayout),
		GraceUntil:  now.Add(domain.LicenseGrace).Format(index.TimeLayout),
	}
	actor := pipeline.Actor{Role: audit.RoleAnonymous, IP: ip}

	if canon, ok := NormalizeKey(req.Key); ok {
		err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
			lic, err := store.LockLicenseByHash(ctx, tx, s.Data.LicenseKeyHash(canon))
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if lic.AppName != req.App {
				v.Result = domain.LicenseWrongApp
				return s.recordRefusal(ctx, tx, actor, lic, v)
			}
			v.Licensee, v.Seats, v.ValidUntil, v.UpdatesUntil = lic.LicenseeName, lic.Seats, lic.ValidUntil, lic.UpdatesUntil
			used := lic.SeatsUsed
			v.SeatsUsed = &used
			switch {
			case lic.Status == "revoked":
				v.Result = domain.LicenseRevoked
				return s.recordRefusal(ctx, tx, actor, lic, v)
			case lic.ValidUntil != "" && now.Format(index.DateLayout) > lic.ValidUntil:
				v.Result = domain.LicenseExpired
				return s.recordRefusal(ctx, tx, actor, lic, v)
			}
			act, err := store.GetActivation(ctx, tx, lic.ID, req.InstanceID)
			switch {
			case err == nil && act.ReleasedAt == nil:
				v.Result = domain.LicenseValid
				return store.TouchActivation(ctx, tx, lic.ID, req.InstanceID, filexVersion)
			case err != nil && !errors.Is(err, store.ErrNotFound):
				return err
			}
			if lic.SeatsUsed >= lic.Seats {
				v.Result = domain.LicenseSeatsExhausted
				return s.recordRefusal(ctx, tx, actor, lic, v)
			}
			if err := store.Activate(ctx, tx, lic.ID, req.InstanceID, filexVersion); err != nil {
				return err
			}
			used++
			v.Result = domain.LicenseValid
			_, err = audit.Append(ctx, tx, s.entry(actor, "license.activate", "license", lic.ID, map[string]any{
				"app": lic.AppName, "key_prefix": lic.KeyPrefix, "instance_id": req.InstanceID,
				"seats": lic.Seats, "seats_used": used, "filex_version": filexVersion,
			}))
			return err
		})
		if err != nil {
			return domain.SignedEnvelope{}, domain.LicenseVerdict{}, err
		}
	}
	if v.Result == domain.LicenseWrongApp || v.Result == domain.LicenseInvalid {
		v.Licensee, v.Seats, v.SeatsUsed, v.ValidUntil, v.UpdatesUntil = "", 0, nil, "", ""
	}
	env, err := signedjson.Sign(s.Keys.License, v)
	if err != nil {
		return domain.SignedEnvelope{}, domain.LicenseVerdict{}, err
	}
	s.log().Info("license check", "app", req.App, "result", v.Result, "ip", ip)
	return env, v, nil
}

// cleanVersion keeps what a filex says its version is (shown with the
// activation), within 64 characters and without control characters; ""
// when it is something else.
func cleanVersion(v string) string {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) > 64 || !utf8.ValidString(v) {
		return ""
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return v
}

// recordRefusal writes a refused check of a known key to the audit log (by
// the license's id and prefix).
func (s *Service) recordRefusal(ctx context.Context, tx pgx.Tx, a pipeline.Actor, lic store.License, v domain.LicenseVerdict) error {
	_, err := audit.Append(ctx, tx, s.entry(a, "license.check_refused", "license", lic.ID, map[string]any{
		"app": lic.AppName, "asked_for": v.App, "key_prefix": lic.KeyPrefix, "instance_id": v.InstanceID,
		"result": string(v.Result),
	}))
	return err
}
