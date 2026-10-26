package notify

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/audit"
	"github.com/brf-tech/filex-apps/backend/internal/datakey"
	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// Limits per account.
const (
	MaxWebhooks = 10
	MaxPushes   = 20
	maxURLBytes = 2048
)

// Refusal is a request the rules refuse; httpapi maps Kind to a status.
type Refusal struct {
	Kind    string // invalid | not_found | conflict | unavailable
	Code    string
	Message string
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Message }

func invalid(code, msg string) error   { return &Refusal{Kind: "invalid", Code: code, Message: msg} }
func notFound(msg string) error        { return &Refusal{Kind: "not_found", Code: "not_found", Message: msg} }
func conflict(code, msg string) error  { return &Refusal{Kind: "conflict", Code: code, Message: msg} }
func unavailable(code, m string) error { return &Refusal{Kind: "unavailable", Code: code, Message: m} }

// ErrDataKeyMissing: channels are sealed with FAPPS_DATA_KEY; without it
// they answer 503 (as licenses and saved instances do).
var ErrDataKeyMissing = unavailable("data_key_missing",
	"FAPPS_DATA_KEY is not set on this server: notification channels are unavailable.")

// Service is the notification channels of each account and the notifier's
// own work (the fan-out and the deliveries run in Worker).
type Service struct {
	Pool *pgxpool.Pool
	Data *datakey.Keys
	// VAPID is the Web Push key pair (nil: browser notifications are off).
	VAPID  *VAPID
	Sender *Sender
	// PublicURL is the store's origin, for the links in messages.
	PublicURL string
	// StoreName is the store's name (FAPPS_STORE_NAME), for the messages'
	// titles and footers; "" reads as the host of PublicURL.
	StoreName string
	// Development allows plain http and private addresses for webhooks (a
	// receiver on the developer's machine); never in production.
	Development bool
	// Resolve looks a webhook's host up when it is saved (nil: the system
	// resolver). The sender checks every address again when it dials.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	Log     *slog.Logger
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// source is the store's host, the messages' footer.
func (s *Service) source() string {
	if u, err := url.Parse(s.PublicURL); err == nil && u.Host != "" {
		return u.Host
	}
	return s.StoreName
}

// name is the store's name in messages.
func (s *Service) name() string {
	if s.StoreName != "" {
		return s.StoreName
	}
	return s.source()
}

// Actor is who changes a channel, for the rules and the audit log.
type Actor struct {
	AccountID int64
	Admin     bool
	Role      string // the audit role: publisher, reviewer, admin
	IP        string
	// Operator is the operator account when an operator command acts for
	// AccountID (the audit row names the operator, and the account it acted
	// for); 0 otherwise.
	Operator int64
}

func (a Actor) entry(action, id string, data map[string]any) audit.Entry {
	acc := a.AccountID
	role := a.Role
	if role == "" {
		role = audit.RolePublisher
	}
	if a.Operator != 0 {
		acc, role = a.Operator, audit.RoleAdmin
		data["for_account"] = a.AccountID
	}
	return audit.Entry{ActorAccountID: &acc, ActorRole: role, Action: action, TargetType: "webhook", TargetID: id,
		IP: a.IP, Data: data}
}

// WebhookView is a webhook as its owner sees it (never its address or
// secrets in full).
type WebhookView struct {
	ID         string
	Name       string
	Format     string
	URLHint    string
	HeaderName string
	Signed     bool
	Events     []string
	CreatedAt  time.Time
	Last       store.ChannelResult
}

func webhookView(w store.Webhook) WebhookView {
	return WebhookView{ID: w.ID, Name: w.Name, Format: w.Format, URLHint: w.URLHint, HeaderName: w.HeaderName,
		Signed: len(w.SecretSealed) > 0, Events: w.Events, CreatedAt: w.CreatedAt, Last: w.Last}
}

// PushView is one of the account's browsers.
type PushView struct {
	ID           string
	EndpointHost string
	UserAgent    string
	CreatedAt    time.Time
	Last         store.ChannelResult
}

func pushView(p store.PushSubscription) PushView {
	return PushView{ID: p.ID, EndpointHost: p.EndpointHost, UserAgent: p.UserAgent, CreatedAt: p.CreatedAt, Last: p.Last}
}

// Settings is everything the notifications page shows.
type Settings struct {
	// Events the account may choose.
	Events []KindInfo
	// PushAvailable: the store has a VAPID key; PushPublicKey is it.
	PushAvailable bool
	PushPublicKey string
	// PushEvents are the events the account's browsers show.
	PushEvents []string
	Pushes     []PushView
	Webhooks   []WebhookView
}

// Settings reads an account's channels and choices.
func (s *Service) Settings(ctx context.Context, account int64, admin bool) (Settings, error) {
	if s.Data == nil {
		return Settings{}, ErrDataKeyMissing
	}
	out := Settings{Events: KindsFor(admin), PushAvailable: s.VAPID != nil}
	if s.VAPID != nil {
		out.PushPublicKey = s.VAPID.PublicKey()
	}
	ev, found, err := store.PushEvents(ctx, s.Pool, account)
	if err != nil {
		return Settings{}, err
	}
	if !found {
		ev = defaultEvents(admin)
	}
	out.PushEvents = ev
	ps, err := store.PushesOf(ctx, s.Pool, account)
	if err != nil {
		return Settings{}, err
	}
	for _, p := range ps {
		out.Pushes = append(out.Pushes, pushView(p))
	}
	ws, err := store.WebhooksOf(ctx, s.Pool, account)
	if err != nil {
		return Settings{}, err
	}
	for _, w := range ws {
		out.Webhooks = append(out.Webhooks, webhookView(w))
	}
	return out, nil
}

// defaultEvents: everything the account may choose.
func defaultEvents(admin bool) []string {
	var out []string
	for _, k := range KindsFor(admin) {
		out = append(out, k.Kind)
	}
	return out
}

// checkEvents validates a chosen set: known kinds, each once, the
// administrators' only for an administrator.
func checkEvents(events []string, admin bool) ([]string, error) {
	out := []string{}
	for _, e := range events {
		if !Known(e) {
			return nil, invalid("invalid_field", "events: "+strconv.Quote(e)+" is not an event of this store.")
		}
		if AdminOnly(e) && !admin {
			return nil, invalid("invalid_field", "events: "+e+" is for administrators.")
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out, nil
}

// SetPushEvents chooses the events the account's browsers show.
func (s *Service) SetPushEvents(ctx context.Context, account int64, admin bool, events []string) ([]string, error) {
	if s.Data == nil {
		return nil, ErrDataKeyMissing
	}
	ev, err := checkEvents(events, admin)
	if err != nil {
		return nil, err
	}
	return ev, store.SetPushEvents(ctx, s.Pool, account, ev)
}

// ----------------------------------------------------------------- push

func pushAAD(id string) []byte { return []byte("push:" + id) }

func (s *Service) openPush(p store.PushSubscription) (PushTarget, error) {
	var t PushTarget
	b, err := s.Data.OpenNotify(p.Sealed, pushAAD(p.ID))
	if err != nil {
		return t, err
	}
	return t, jsonUnmarshal(b, &t)
}

// checkEndpoint validates a push endpoint as the browser gave it.
func (s *Service) checkEndpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || len(raw) > maxURLBytes || u.User != nil {
		return nil, invalid("invalid_field", "endpoint is not a push service address.")
	}
	if u.Scheme != "https" && !(s.Development && u.Scheme == "http") {
		return nil, invalid("invalid_field", "endpoint must be an https address.")
	}
	if a, err := netip.ParseAddr(strings.Trim(u.Hostname(), "[]")); err == nil && !s.Development && safehttp.PublicOnly(a) != nil {
		return nil, invalid("address_not_public", "endpoint is not a public address.")
	}
	return u, nil
}

// SubscribePush keeps a browser's subscription for the account. The same
// browser subscribing again (the same endpoint) updates its row, for
// whichever account subscribes it now. created is false then.
func (s *Service) SubscribePush(ctx context.Context, account int64, admin bool, t PushTarget, userAgent string) (PushView, bool, error) {
	if s.Data == nil {
		return PushView{}, false, ErrDataKeyMissing
	}
	if s.VAPID == nil {
		return PushView{}, false, unavailable("push_unavailable", "Browser notifications are not set up on this store.")
	}
	u, err := s.checkEndpoint(t.Endpoint)
	if err != nil {
		return PushView{}, false, err
	}
	if _, _, err := t.keys(); err != nil {
		return PushView{}, false, invalid("invalid_field", err.Error())
	}
	t.Endpoint = u.String()
	plain, err := marshal(t)
	if err != nil {
		return PushView{}, false, err
	}
	if utf8.RuneCountInString(userAgent) > 200 {
		userAgent = string([]rune(userAgent)[:200])
	}
	index := s.Data.PushIndex(t.Endpoint)
	var out store.PushSubscription
	var created bool
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		row := store.PushSubscription{AccountID: account, EndpointIndex: index, EndpointHost: u.Hostname(), UserAgent: userAgent}
		existing, err := store.PushByIndex(ctx, tx, index)
		switch {
		case err == nil:
			row.ID = existing.ID
			if row.Sealed, err = s.Data.SealNotify(plain, pushAAD(row.ID)); err != nil {
				return err
			}
			if err := store.ReplacePush(ctx, tx, row); err != nil {
				return err
			}
		case errors.Is(err, store.ErrNotFound):
			n, err := store.CountPushes(ctx, tx, account)
			if err != nil {
				return err
			}
			if n >= MaxPushes {
				return conflict("pushes_full", "At most "+strconv.Itoa(MaxPushes)+" browsers can get notifications; remove one first.")
			}
			if row.ID, err = store.NewID(ctx, tx); err != nil {
				return err
			}
			if row.Sealed, err = s.Data.SealNotify(plain, pushAAD(row.ID)); err != nil {
				return err
			}
			if err := store.InsertPush(ctx, tx, row); err != nil {
				return err
			}
			created = true
		default:
			return err
		}
		if _, found, err := store.PushEvents(ctx, tx, account); err != nil {
			return err
		} else if !found {
			if err := store.SetPushEvents(ctx, tx, account, defaultEvents(admin)); err != nil {
				return err
			}
		}
		out, err = store.PushOf(ctx, tx, account, row.ID)
		return err
	})
	if errors.Is(err, store.ErrConflict) {
		// Two subscriptions of one browser at the same moment: the other
		// one's row is there now.
		return s.SubscribePush(ctx, account, admin, t, userAgent)
	}
	if err != nil {
		return PushView{}, false, err
	}
	return pushView(out), created, nil
}

// UnsubscribePush forgets one of the account's browsers.
func (s *Service) UnsubscribePush(ctx context.Context, account int64, id string) error {
	if !uuidLike(id) {
		return notFound("No such browser subscription.")
	}
	ok, err := store.DeletePush(ctx, s.Pool, account, id)
	if err != nil {
		return err
	}
	if !ok {
		return notFound("No such browser subscription.")
	}
	return nil
}

// TestPush sends a test notification to one of the account's browsers, now.
func (s *Service) TestPush(ctx context.Context, account int64, id string) (Result, error) {
	if s.Data == nil {
		return Result{}, ErrDataKeyMissing
	}
	if !uuidLike(id) {
		return Result{}, notFound("No such browser subscription.")
	}
	p, err := store.PushOf(ctx, s.Pool, account, id)
	if errors.Is(err, store.ErrNotFound) {
		return Result{}, notFound("No such browser subscription.")
	}
	if err != nil {
		return Result{}, err
	}
	t, err := s.openPush(p)
	if err != nil {
		return Result{}, err
	}
	m := Render(KindTest, mustJSON(map[string]any{"channel": "browser notifications on this device"}), s.PublicURL, s.name())
	res := s.Sender.SendPush(ctx, s.VAPID, t, m, "test")
	if res.Gone {
		_ = store.DeletePushByID(ctx, s.Pool, p.ID)
		return res, nil
	}
	_ = store.SetPushResult(ctx, s.Pool, p.ID, res.Status, res.HTTPStatus, res.Error)
	return res, nil
}

// ----------------------------------------------------------------- webhooks

func hookAAD(id, field string) []byte { return []byte("webhook:" + id + ":" + field) }

// WebhookCreate is a new webhook.
type WebhookCreate struct {
	Name        string
	Format      string
	URL         string
	HeaderName  string
	HeaderValue string
	Events      []string
}

// WebhookCreated is the answer: the webhook and, for the generic format,
// its signing secret, shown this once.
type WebhookCreated struct {
	Webhook WebhookView
	Secret  string
}

// checkWebhookURL validates the address a person typed: https (http only
// in development), a host, no credentials, at most 2048 bytes, and - when
// the host is an address or resolves now - a public one.
func (s *Service) checkWebhookURL(ctx context.Context, raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || len(raw) > maxURLBytes {
		return nil, invalid("invalid_field", "url is not a web address.")
	}
	if u.User != nil {
		return nil, invalid("invalid_field", "url must not carry a user name or password; use the header instead.")
	}
	if u.Scheme != "https" && !(s.Development && u.Scheme == "http") {
		return nil, invalid("invalid_field", "url must be an https address.")
	}
	u.Fragment = ""
	if s.Development {
		return u, nil
	}
	host := strings.Trim(u.Hostname(), "[]")
	if a, err := netip.ParseAddr(host); err == nil {
		if safehttp.PublicOnly(a) != nil {
			return nil, invalid("address_not_public", "url points at a private, loopback or reserved address; the store only calls public addresses.")
		}
		return u, nil
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return nil, invalid("address_not_public", "url points at this machine; the store only calls public addresses.")
	}
	resolve := s.Resolve
	if resolve == nil {
		resolve = func(ctx context.Context, h string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", h)
		}
	}
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := resolve(rctx, host)
	if err == nil {
		for _, a := range addrs {
			if safehttp.PublicOnly(a) != nil {
				return nil, invalid("address_not_public", "url's host resolves to a private, loopback or reserved address; the store only calls public addresses.")
			}
		}
	}
	return u, nil
}

// urlHint is what the list shows of an address: the scheme and the host,
// the rest cut (a Slack or Discord address is a credential).
func urlHint(u *url.URL) string {
	h := u.Scheme + "://" + u.Host
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		h += "/…"
	}
	return h
}

// newSecret is a generic webhook's signing secret.
func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "whsec_" + b64.EncodeToString(b), nil
}

func cleanName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n < 1 || n > 80 {
		return "", invalid("invalid_field", "name must be 1 to 80 characters.")
	}
	for _, r := range s {
		if r < ' ' || r == 0x7f {
			return "", invalid("invalid_field", "name holds a control character.")
		}
	}
	return s, nil
}

// CreateWebhook adds a webhook to the account.
func (s *Service) CreateWebhook(ctx context.Context, a Actor, in WebhookCreate) (WebhookCreated, error) {
	account, admin := a.AccountID, a.Admin
	if s.Data == nil {
		return WebhookCreated{}, ErrDataKeyMissing
	}
	name, err := cleanName(in.Name)
	if err != nil {
		return WebhookCreated{}, err
	}
	if !ValidFormat(in.Format) {
		return WebhookCreated{}, invalid("invalid_field", "format must be generic, slack, discord or teams.")
	}
	u, err := s.checkWebhookURL(ctx, in.URL)
	if err != nil {
		return WebhookCreated{}, err
	}
	events, err := checkEvents(in.Events, admin)
	if err != nil {
		return WebhookCreated{}, err
	}
	in.HeaderName = strings.TrimSpace(in.HeaderName)
	if in.Format != FormatGeneric && (in.HeaderName != "" || in.HeaderValue != "") {
		return WebhookCreated{}, invalid("invalid_field", "a header is for the generic format only.")
	}
	if (in.HeaderName == "") != (in.HeaderValue == "") {
		return WebhookCreated{}, invalid("invalid_field", "header_name and header_value go together.")
	}
	if in.HeaderName != "" && !ValidHeaderName(in.HeaderName) {
		return WebhookCreated{}, invalid("invalid_field", "header_name is not a header the store may send (an HTTP token, not Host, Content-Type, Cookie or X-Fapps-*).")
	}
	if in.HeaderValue != "" && !ValidHeaderValue(in.HeaderValue) {
		return WebhookCreated{}, invalid("invalid_field", "header_value must be 1 to 1024 printable characters on one line.")
	}
	var out WebhookCreated
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		n, err := store.CountWebhooks(ctx, tx, account)
		if err != nil {
			return err
		}
		if n >= MaxWebhooks {
			return conflict("webhooks_full", "At most "+strconv.Itoa(MaxWebhooks)+" webhooks per account; delete one first.")
		}
		w := store.Webhook{AccountID: account, Name: name, Format: in.Format, URLHint: urlHint(u), Events: events}
		if w.ID, err = store.NewID(ctx, tx); err != nil {
			return err
		}
		if w.URLSealed, err = s.Data.SealNotify([]byte(u.String()), hookAAD(w.ID, "url")); err != nil {
			return err
		}
		if in.Format == FormatGeneric {
			secret, err := newSecret()
			if err != nil {
				return err
			}
			if w.SecretSealed, err = s.Data.SealNotify([]byte(secret), hookAAD(w.ID, "secret")); err != nil {
				return err
			}
			out.Secret = secret
		}
		if in.HeaderName != "" {
			w.HeaderName = in.HeaderName
			if w.HeaderSealed, err = s.Data.SealNotify([]byte(in.HeaderValue), hookAAD(w.ID, "header")); err != nil {
				return err
			}
		}
		if err := store.InsertWebhook(ctx, tx, w); err != nil {
			return err
		}
		// The address is a credential for Slack and Discord: the log keeps
		// its hint only.
		if _, err := audit.Append(ctx, tx, a.entry("webhook.create", w.ID, map[string]any{
			"format": w.Format, "url_hint": w.URLHint, "header_name": w.HeaderName, "events": events})); err != nil {
			return err
		}
		row, err := store.WebhookOf(ctx, tx, account, w.ID)
		out.Webhook = webhookView(row)
		return err
	})
	if err != nil {
		return WebhookCreated{}, err
	}
	return out, nil
}

// WebhookPatch changes a webhook's name and events (nil: unchanged).
type WebhookPatch struct {
	Name   *string
	Events []string
	// EventsSet distinguishes "no events" from "events not given".
	EventsSet bool
}

// UpdateWebhook renames a webhook or changes its events.
func (s *Service) UpdateWebhook(ctx context.Context, account int64, admin bool, id string, p WebhookPatch) (WebhookView, error) {
	if s.Data == nil {
		return WebhookView{}, ErrDataKeyMissing
	}
	if !uuidLike(id) {
		return WebhookView{}, notFound("No such webhook.")
	}
	w, err := store.WebhookOf(ctx, s.Pool, account, id)
	if errors.Is(err, store.ErrNotFound) {
		return WebhookView{}, notFound("No such webhook.")
	}
	if err != nil {
		return WebhookView{}, err
	}
	name, events := w.Name, w.Events
	if p.Name != nil {
		if name, err = cleanName(*p.Name); err != nil {
			return WebhookView{}, err
		}
	}
	if p.EventsSet {
		if events, err = checkEvents(p.Events, admin); err != nil {
			return WebhookView{}, err
		}
	}
	if _, err := store.UpdateWebhook(ctx, s.Pool, account, id, name, events); err != nil {
		return WebhookView{}, err
	}
	w, err = store.WebhookOf(ctx, s.Pool, account, id)
	return webhookView(w), err
}

// DeleteWebhook removes one of the account's webhooks.
func (s *Service) DeleteWebhook(ctx context.Context, a Actor, id string) error {
	if !uuidLike(id) {
		return notFound("No such webhook.")
	}
	return db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		ok, err := store.DeleteWebhook(ctx, tx, a.AccountID, id)
		if err != nil {
			return err
		}
		if !ok {
			return notFound("No such webhook.")
		}
		_, err = audit.Append(ctx, tx, a.entry("webhook.delete", id, map[string]any{}))
		return err
	})
}

// openWebhook opens a webhook's sealed fields.
func (s *Service) openWebhook(w store.Webhook) (Webhook, error) {
	out := Webhook{ID: w.ID, Name: w.Name, Format: w.Format, HeaderName: w.HeaderName}
	u, err := s.Data.OpenNotify(w.URLSealed, hookAAD(w.ID, "url"))
	if err != nil {
		return out, err
	}
	out.URL = string(u)
	if len(w.SecretSealed) > 0 {
		if out.Secret, err = s.Data.OpenNotify(w.SecretSealed, hookAAD(w.ID, "secret")); err != nil {
			return out, err
		}
	}
	if len(w.HeaderSealed) > 0 {
		v, err := s.Data.OpenNotify(w.HeaderSealed, hookAAD(w.ID, "header"))
		if err != nil {
			return out, err
		}
		out.HeaderValue = string(v)
	}
	return out, nil
}

// testDelivery names a test delivery ("test-<unix nanos>").
func testDelivery() string { return "test-" + strconv.FormatInt(time.Now().UnixNano(), 10) }

// TestWebhook sends a test message to one of the account's webhooks, now,
// once (no retry), and records the outcome as its last delivery.
func (s *Service) TestWebhook(ctx context.Context, account int64, id string) (Result, error) {
	if s.Data == nil {
		return Result{}, ErrDataKeyMissing
	}
	if !uuidLike(id) {
		return Result{}, notFound("No such webhook.")
	}
	w, err := store.WebhookOf(ctx, s.Pool, account, id)
	if errors.Is(err, store.ErrNotFound) {
		return Result{}, notFound("No such webhook.")
	}
	if err != nil {
		return Result{}, err
	}
	open, err := s.openWebhook(w)
	if err != nil {
		return Result{}, err
	}
	data := mustJSON(map[string]any{"channel": "the webhook " + strconv.Quote(w.Name)})
	m := Render(KindTest, data, s.PublicURL, s.name())
	res := s.Sender.SendWebhook(ctx, open, m, Meta{Delivery: testDelivery(), Event: KindTest, OccurredAt: time.Now(),
		Source: s.source(), Store: s.name(), Data: data})
	if err := store.SetWebhookResult(ctx, s.Pool, w.ID, res.Status, res.HTTPStatus, res.Error); err != nil {
		s.log().Error("notify: record a test", "webhook", w.ID, "err", err)
	}
	return res, nil
}

// uuidLike is the shape of a channel's id (anything else is a 404 without
// asking the database).
func uuidLike(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
				return false
			}
		}
	}
	return true
}
