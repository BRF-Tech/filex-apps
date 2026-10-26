package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Notifications (migration 0011, internal/notify): a person's webhooks and
// browser subscriptions, the events the store raises, their deliveries.

// Job kinds of the notifier.
const (
	JobNotifyEvent   = "notify_event"   // fan an event out to its channels
	JobNotifyDeliver = "notify_deliver" // send one delivery
)

// NewID is a fresh uuid from the database (rows whose sealed fields are
// bound to their id are given it before they are written).
func NewID(ctx context.Context, q Q) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id)
	return id, err
}

// ChannelResult is a channel's last delivery or test.
type ChannelResult struct {
	Status     string // "delivered" | "failed" | "" (never)
	HTTPStatus *int
	Error      string
	At         *time.Time
}

// Webhook is one row of notification_webhooks.
type Webhook struct {
	ID           string
	AccountID    int64
	Name         string
	Format       string
	URLSealed    []byte
	URLHint      string
	SecretSealed []byte
	HeaderName   string
	HeaderSealed []byte
	Events       []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Last         ChannelResult
}

const webhookCols = `id::text, account_id, name, format, url_sealed, url_hint, secret_sealed, COALESCE(header_name, ''),
	header_sealed, events, created_at, updated_at, COALESCE(last_status, ''), last_http_status, COALESCE(last_error, ''), last_at`

func scanWebhook(row pgx.Row) (Webhook, error) {
	var w Webhook
	err := row.Scan(&w.ID, &w.AccountID, &w.Name, &w.Format, &w.URLSealed, &w.URLHint, &w.SecretSealed, &w.HeaderName,
		&w.HeaderSealed, &w.Events, &w.CreatedAt, &w.UpdatedAt, &w.Last.Status, &w.Last.HTTPStatus, &w.Last.Error, &w.Last.At)
	if w.Events == nil {
		w.Events = []string{}
	}
	return w, notFound(err)
}

// InsertWebhook stores a webhook (its ID from NewID).
func InsertWebhook(ctx context.Context, q Q, w Webhook) error {
	var header *string
	if w.HeaderName != "" {
		header = &w.HeaderName
	}
	_, err := q.Exec(ctx, `
		INSERT INTO notification_webhooks (id, account_id, name, format, url_sealed, url_hint, secret_sealed, header_name,
		                                   header_sealed, events)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		w.ID, w.AccountID, w.Name, w.Format, w.URLSealed, w.URLHint, w.SecretSealed, header, w.HeaderSealed, nonNilStrings(w.Events))
	return err
}

// WebhooksOf lists an account's webhooks, oldest first.
func WebhooksOf(ctx context.Context, q Q, account int64) ([]Webhook, error) {
	rows, err := q.Query(ctx, `SELECT `+webhookCols+` FROM notification_webhooks WHERE account_id = $1 ORDER BY created_at, id`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Webhook{}
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WebhookOf is one of the account's webhooks (ErrNotFound for another's).
func WebhookOf(ctx context.Context, q Q, account int64, id string) (Webhook, error) {
	return scanWebhook(q.QueryRow(ctx, `SELECT `+webhookCols+` FROM notification_webhooks WHERE id = $1::uuid AND account_id = $2`, id, account))
}

// WebhookByID is a webhook whoever it belongs to (the notifier).
func WebhookByID(ctx context.Context, q Q, id string) (Webhook, error) {
	return scanWebhook(q.QueryRow(ctx, `SELECT `+webhookCols+` FROM notification_webhooks WHERE id = $1::uuid`, id))
}

// CountWebhooks counts an account's webhooks.
func CountWebhooks(ctx context.Context, q Q, account int64) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM notification_webhooks WHERE account_id = $1`, account).Scan(&n)
	return n, err
}

// UpdateWebhook renames a webhook and sets its events; false when the
// account has no such webhook.
func UpdateWebhook(ctx context.Context, q Q, account int64, id, name string, events []string) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE notification_webhooks SET name = $3, events = $4, updated_at = now()
		WHERE id = $1::uuid AND account_id = $2`, id, account, name, nonNilStrings(events))
	return tag.RowsAffected() == 1, err
}

// DeleteWebhook removes one of the account's webhooks (its deliveries go
// with it).
func DeleteWebhook(ctx context.Context, q Q, account int64, id string) (bool, error) {
	tag, err := q.Exec(ctx, `DELETE FROM notification_webhooks WHERE id = $1::uuid AND account_id = $2`, id, account)
	return tag.RowsAffected() == 1, err
}

// SetWebhookResult records a webhook's last delivery or test.
func SetWebhookResult(ctx context.Context, q Q, id, status string, httpStatus int, msg string) error {
	_, err := q.Exec(ctx, `
		UPDATE notification_webhooks SET last_status = $2, last_http_status = $3, last_error = NULLIF($4, ''), last_at = now()
		WHERE id = $1::uuid`, id, status, nullInt(httpStatus), truncate(msg, 500))
	return err
}

// PushSubscription is one row of push_subscriptions.
type PushSubscription struct {
	ID            string
	AccountID     int64
	EndpointIndex []byte
	Sealed        []byte
	EndpointHost  string
	UserAgent     string
	CreatedAt     time.Time
	Last          ChannelResult
}

const pushCols = `id::text, account_id, endpoint_index, sealed, endpoint_host, user_agent, created_at,
	COALESCE(last_status, ''), last_http_status, COALESCE(last_error, ''), last_at`

func scanPush(row pgx.Row) (PushSubscription, error) {
	var p PushSubscription
	err := row.Scan(&p.ID, &p.AccountID, &p.EndpointIndex, &p.Sealed, &p.EndpointHost, &p.UserAgent, &p.CreatedAt,
		&p.Last.Status, &p.Last.HTTPStatus, &p.Last.Error, &p.Last.At)
	return p, notFound(err)
}

// PushByIndex is the subscription of an endpoint (any account's).
func PushByIndex(ctx context.Context, q Q, index []byte) (PushSubscription, error) {
	return scanPush(q.QueryRow(ctx, `SELECT `+pushCols+` FROM push_subscriptions WHERE endpoint_index = $1`, index))
}

// InsertPush stores a subscription (its ID from NewID). ErrConflict when the
// endpoint is already stored.
func InsertPush(ctx context.Context, q Q, p PushSubscription) error {
	_, err := q.Exec(ctx, `
		INSERT INTO push_subscriptions (id, account_id, endpoint_index, sealed, endpoint_host, user_agent)
		VALUES ($1::uuid, $2, $3, $4, $5, $6)`, p.ID, p.AccountID, p.EndpointIndex, p.Sealed, p.EndpointHost, p.UserAgent)
	if err != nil && isUnique(err) {
		return ErrConflict
	}
	return err
}

// ReplacePush gives an existing subscription row (the same endpoint
// subscribing again, maybe for another account) new keys and owner.
func ReplacePush(ctx context.Context, q Q, p PushSubscription) error {
	_, err := q.Exec(ctx, `
		UPDATE push_subscriptions SET account_id = $2, sealed = $3, endpoint_host = $4, user_agent = $5,
		       last_status = NULL, last_http_status = NULL, last_error = NULL, last_at = NULL
		WHERE id = $1::uuid`, p.ID, p.AccountID, p.Sealed, p.EndpointHost, p.UserAgent)
	return err
}

// PushesOf lists an account's browser subscriptions, oldest first.
func PushesOf(ctx context.Context, q Q, account int64) ([]PushSubscription, error) {
	rows, err := q.Query(ctx, `SELECT `+pushCols+` FROM push_subscriptions WHERE account_id = $1 ORDER BY created_at, id`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PushSubscription{}
	for rows.Next() {
		p, err := scanPush(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PushOf is one of the account's subscriptions.
func PushOf(ctx context.Context, q Q, account int64, id string) (PushSubscription, error) {
	return scanPush(q.QueryRow(ctx, `SELECT `+pushCols+` FROM push_subscriptions WHERE id = $1::uuid AND account_id = $2`, id, account))
}

// PushByID is a subscription whoever it belongs to (the notifier).
func PushByID(ctx context.Context, q Q, id string) (PushSubscription, error) {
	return scanPush(q.QueryRow(ctx, `SELECT `+pushCols+` FROM push_subscriptions WHERE id = $1::uuid`, id))
}

// CountPushes counts an account's subscriptions.
func CountPushes(ctx context.Context, q Q, account int64) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM push_subscriptions WHERE account_id = $1`, account).Scan(&n)
	return n, err
}

// DeletePush removes one of the account's subscriptions.
func DeletePush(ctx context.Context, q Q, account int64, id string) (bool, error) {
	tag, err := q.Exec(ctx, `DELETE FROM push_subscriptions WHERE id = $1::uuid AND account_id = $2`, id, account)
	return tag.RowsAffected() == 1, err
}

// DeletePushByID removes a subscription the push service says is gone.
func DeletePushByID(ctx context.Context, q Q, id string) error {
	_, err := q.Exec(ctx, `DELETE FROM push_subscriptions WHERE id = $1::uuid`, id)
	return err
}

// SetPushResult records a subscription's last delivery or test.
func SetPushResult(ctx context.Context, q Q, id, status string, httpStatus int, msg string) error {
	_, err := q.Exec(ctx, `
		UPDATE push_subscriptions SET last_status = $2, last_http_status = $3, last_error = NULLIF($4, ''), last_at = now()
		WHERE id = $1::uuid`, id, status, nullInt(httpStatus), truncate(msg, 500))
	return err
}

// PushEvents are the events an account's browsers show (nil, false when the
// account never chose).
func PushEvents(ctx context.Context, q Q, account int64) ([]string, bool, error) {
	var ev []string
	err := q.QueryRow(ctx, `SELECT push_events FROM notification_preferences WHERE account_id = $1`, account).Scan(&ev)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if ev == nil {
		ev = []string{}
	}
	return ev, err == nil, err
}

// SetPushEvents sets them.
func SetPushEvents(ctx context.Context, q Q, account int64, events []string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO notification_preferences (account_id, push_events) VALUES ($1, $2)
		ON CONFLICT (account_id) DO UPDATE SET push_events = EXCLUDED.push_events, updated_at = now()`,
		account, nonNilStrings(events))
	return err
}

// NotifyEvent is one row of notification_events.
type NotifyEvent struct {
	ID         int64
	Kind       string
	Audience   string // admin | accounts | all
	AccountIDs []int64
	DedupeKey  string
	Data       json.RawMessage
	CreatedAt  time.Time
}

// InsertNotifyEvent stores an event; inserted is false when its dedupe key
// was already taken (the event already happened).
func InsertNotifyEvent(ctx context.Context, q Q, ev NotifyEvent) (int64, bool, error) {
	var key *string
	if ev.DedupeKey != "" {
		key = &ev.DedupeKey
	}
	ids := ev.AccountIDs
	if ids == nil {
		ids = []int64{}
	}
	var id int64
	err := q.QueryRow(ctx, `
		INSERT INTO notification_events (kind, audience, account_ids, dedupe_key, data) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (dedupe_key) DO NOTHING RETURNING id`, ev.Kind, ev.Audience, ids, key, []byte(ev.Data)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// NotifyEventByID reads an event.
func NotifyEventByID(ctx context.Context, q Q, id int64) (NotifyEvent, error) {
	var ev NotifyEvent
	var key *string
	err := q.QueryRow(ctx, `SELECT id, kind, audience, account_ids, dedupe_key, data, created_at FROM notification_events WHERE id = $1`, id).
		Scan(&ev.ID, &ev.Kind, &ev.Audience, &ev.AccountIDs, &key, &ev.Data, &ev.CreatedAt)
	if key != nil {
		ev.DedupeKey = *key
	}
	return ev, notFound(err)
}

// Recipient is one channel an event goes to.
type Recipient struct {
	AccountID int64
	WebhookID string // one of the two
	PushID    string
}

// NotifyRecipients are the channels that chose kind, of the given accounts
// (nil: of every account), never of a disabled account.
func NotifyRecipients(ctx context.Context, q Q, kind string, accounts []int64) ([]Recipient, error) {
	rows, err := q.Query(ctx, `
		SELECT w.account_id, w.id::text, ''
		FROM notification_webhooks w JOIN accounts a ON a.id = w.account_id
		WHERE a.disabled_at IS NULL AND $1 = ANY (w.events) AND ($2::bigint[] IS NULL OR w.account_id = ANY ($2))
		UNION ALL
		SELECT p.account_id, '', p.id::text
		FROM push_subscriptions p
		JOIN notification_preferences np ON np.account_id = p.account_id
		JOIN accounts a ON a.id = p.account_id
		WHERE a.disabled_at IS NULL AND $1 = ANY (np.push_events) AND ($2::bigint[] IS NULL OR p.account_id = ANY ($2))
		ORDER BY 1, 2, 3`, kind, accounts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Recipient
	for rows.Next() {
		var r Recipient
		if err := rows.Scan(&r.AccountID, &r.WebhookID, &r.PushID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertDelivery records that an event goes to a channel; inserted is false
// when it was already recorded (a fan-out run again).
func InsertDelivery(ctx context.Context, q Q, event int64, r Recipient) (int64, bool, error) {
	var id int64
	err := q.QueryRow(ctx, `
		INSERT INTO notification_deliveries (event_id, webhook_id, push_id) VALUES ($1, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid)
		ON CONFLICT DO NOTHING RETURNING id`, event, r.WebhookID, r.PushID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// Delivery is one row of notification_deliveries.
type Delivery struct {
	ID         int64
	EventID    int64
	WebhookID  string
	PushID     string
	Status     string
	Attempts   int
	HTTPStatus *int
	LastError  string
}

// DeliveryByID reads a delivery; ErrNotFound when its channel was removed
// (the row went with it).
func DeliveryByID(ctx context.Context, q Q, id int64) (Delivery, error) {
	var d Delivery
	err := q.QueryRow(ctx, `
		SELECT id, event_id, COALESCE(webhook_id::text, ''), COALESCE(push_id::text, ''), status, attempts, http_status,
		       COALESCE(last_error, '')
		FROM notification_deliveries WHERE id = $1`, id).
		Scan(&d.ID, &d.EventID, &d.WebhookID, &d.PushID, &d.Status, &d.Attempts, &d.HTTPStatus, &d.LastError)
	return d, notFound(err)
}

// SetDeliveryResult records an attempt: status queued (to be tried again),
// delivered or failed.
func SetDeliveryResult(ctx context.Context, q Q, id int64, status string, httpStatus int, msg string) error {
	_, err := q.Exec(ctx, `
		UPDATE notification_deliveries SET status = $2, attempts = attempts + 1, http_status = $3,
		       last_error = NULLIF($4, ''), updated_at = now()
		WHERE id = $1`, id, status, nullInt(httpStatus), truncate(msg, 500))
	return err
}

// DeliveriesOf lists an event's deliveries (tests, the operator).
func DeliveriesOf(ctx context.Context, q Q, event int64) ([]Delivery, error) {
	rows, err := q.Query(ctx, `
		SELECT id, event_id, COALESCE(webhook_id::text, ''), COALESCE(push_id::text, ''), status, attempts, http_status,
		       COALESCE(last_error, '')
		FROM notification_deliveries WHERE event_id = $1 ORDER BY id`, event)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.EventID, &d.WebhookID, &d.PushID, &d.Status, &d.Attempts, &d.HTTPStatus, &d.LastError); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// NotifyState reads one of the notifier's facts.
func NotifyState(ctx context.Context, q Q, key string) (string, bool, error) {
	var v string
	err := q.QueryRow(ctx, `SELECT value FROM notification_state WHERE key = $1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

// SetNotifyState writes one.
func SetNotifyState(ctx context.Context, q Q, key, value string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO notification_state (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value)
	return err
}

// GitHubIdentity is an account's GitHub user id and login (the role check
// asks GitHub about them). ErrNotFound for an account without one (the
// operator).
func GitHubIdentity(ctx context.Context, q Q, account int64) (int64, string, error) {
	var subject, login string
	err := q.QueryRow(ctx, `
		SELECT subject, login FROM identities WHERE account_id = $1 AND provider = 'github'
		ORDER BY last_login_at DESC NULLS LAST, id DESC LIMIT 1`, account).Scan(&subject, &login)
	if err != nil {
		return 0, "", notFound(err)
	}
	id, err := strconv.ParseInt(subject, 10, 64)
	if err != nil {
		return 0, "", ErrNotFound
	}
	return id, login, nil
}

// AccountByGitHubLogin is the account that signs in as a GitHub login
// (case-insensitive, as GitHub treats logins).
func AccountByGitHubLogin(ctx context.Context, q Q, login string) (int64, error) {
	var id int64
	err := q.QueryRow(ctx, `
		SELECT account_id FROM identities WHERE provider = 'github' AND lower(login) = lower($1)
		ORDER BY last_login_at DESC NULLS LAST, id DESC LIMIT 1`, login).Scan(&id)
	return id, notFound(err)
}

// PublisherMemberIDs are the accounts of a publisher's members.
func PublisherMemberIDs(ctx context.Context, q Q, publisher string) ([]int64, error) {
	rows, err := q.Query(ctx, `SELECT account_id FROM publisher_members WHERE publisher_id = $1 ORDER BY account_id`, publisher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AppNameExists reports whether an app of that name is in the catalog (in
// any status).
func AppNameExists(ctx context.Context, q Q, name string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM apps WHERE name = $1)`, name).Scan(&ok)
	return ok, err
}

func nullInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
