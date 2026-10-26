// Package audit writes and checks the append-only audit log. Each row
// carries the hash of the one before it: a row removed, inserted out of turn
// or rewritten (the table's trigger refuses UPDATE and DELETE, but a
// superuser could disable it) shows as a broken chain.
//
// A row's hash is the lower-hex sha256 of a canonical JSON object of its
// fields and the previous row's hash. Everything that goes in round-trips
// through PostgreSQL unchanged: the time is cut to microseconds, the address
// is normalized by Go, and data is canonical JSON (sorted keys, no HTML
// escaping). Keep floats out of data: jsonb may print them differently.
package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/brf-tech/filex-apps/backend/internal/db"
)

// Roles an actor may act in (the audit_log CHECK).
const (
	RoleAnonymous = "anonymous"
	RolePublisher = "publisher"
	RoleReviewer  = "reviewer"
	RoleAdmin     = "admin"
	RoleSystem    = "system"
)

// Entry is what a caller records.
type Entry struct {
	ActorAccountID *int64
	ActorRole      string
	Action         string // "<target>.<verb>", e.g. "submission.approve"
	TargetType     string
	TargetID       string
	IP             string
	Data           any // a JSON object; nil is {}
}

// Row is a stored entry.
type Row struct {
	ID             int64
	At             time.Time
	ActorAccountID *int64
	Actor          string // the actor's display name (listing only)
	ActorRole      string
	Action         string
	TargetType     string
	TargetID       string
	IP             string
	Data           json.RawMessage
	PrevHash       string
	Hash           string
}

const timeLayout = "2006-01-02T15:04:05.000000Z"

// hashed is the canonical object a row's hash covers (field order fixed by
// the struct; data canonical).
type hashed struct {
	Prev       string          `json:"prev"`
	At         string          `json:"at"`
	Actor      *int64          `json:"actor"`
	Role       string          `json:"role"`
	Action     string          `json:"action"`
	TargetType string          `json:"target_type"`
	TargetID   string          `json:"target_id"`
	IP         string          `json:"ip"`
	Data       json.RawMessage `json:"data"`
}

// Hash computes a row's hash given the previous row's hash ("" for the
// first row).
func Hash(prev string, r Row) (string, error) {
	data, err := Canonical(r.Data)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(hashed{
		Prev: prev, At: r.At.UTC().Format(timeLayout), Actor: r.ActorAccountID, Role: r.ActorRole,
		Action: r.Action, TargetType: r.TargetType, TargetID: r.TargetID, IP: NormalizeIP(r.IP), Data: data,
	}); err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n"))
	return hex.EncodeToString(sum[:]), nil
}

// Canonical re-encodes a JSON value with sorted keys, no HTML escaping and
// no insignificant space. Empty input is {}.
func Canonical(raw []byte) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return json.RawMessage("{}"), nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// NormalizeIP gives the one text form of an address ("" when it is none).
func NormalizeIP(s string) string {
	if s == "" {
		return ""
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return ""
	}
	return a.Unmap().String()
}

// Append writes e as the next row of the chain. It must run inside the
// caller's transaction (the row commits or rolls back with the action it
// records); it takes the audit advisory lock until that transaction ends.
func Append(ctx context.Context, tx pgx.Tx, e Entry) (Row, error) {
	if e.Action == "" || e.ActorRole == "" {
		return Row{}, errors.New("audit: action and actor role are required")
	}
	raw, err := json.Marshal(e.Data)
	if err != nil {
		return Row{}, fmt.Errorf("audit data: %w", err)
	}
	data, err := Canonical(raw)
	if err != nil {
		return Row{}, err
	}
	if !bytes.HasPrefix(data, []byte("{")) {
		return Row{}, errors.New("audit: data must be a JSON object")
	}
	if err := db.XactLock(ctx, tx, db.LockAudit); err != nil {
		return Row{}, err
	}
	var prev string
	err = tx.QueryRow(ctx, `SELECT hash FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&prev)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Row{}, err
	}
	r := Row{
		At: time.Now().UTC().Truncate(time.Microsecond), ActorAccountID: e.ActorAccountID, ActorRole: e.ActorRole,
		Action: e.Action, TargetType: e.TargetType, TargetID: e.TargetID, IP: NormalizeIP(e.IP), Data: data, PrevHash: prev,
	}
	if r.Hash, err = Hash(prev, r); err != nil {
		return Row{}, err
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO audit_log (at, actor_account_id, actor_role, action, target_type, target_id, ip, data, prev_hash, hash)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), NULLIF($7, '')::inet, $8, NULLIF($9, ''), $10)
		RETURNING id`,
		r.At, r.ActorAccountID, r.ActorRole, r.Action, r.TargetType, r.TargetID, r.IP, []byte(r.Data), r.PrevHash, r.Hash).Scan(&r.ID)
	return r, err
}

const rowCols = `l.id, l.at, l.actor_account_id, COALESCE(a.display_name, ''), l.actor_role, l.action,
	COALESCE(l.target_type, ''), COALESCE(l.target_id, ''), COALESCE(host(l.ip), ''), l.data,
	COALESCE(l.prev_hash, ''), l.hash`

func scanRow(row interface{ Scan(...any) error }) (Row, error) {
	var r Row
	var data []byte
	err := row.Scan(&r.ID, &r.At, &r.ActorAccountID, &r.Actor, &r.ActorRole, &r.Action,
		&r.TargetType, &r.TargetID, &r.IP, &data, &r.PrevHash, &r.Hash)
	r.Data = json.RawMessage(data)
	return r, err
}

// VerifyChain checks rows in id order: each row's prev_hash is the hash of
// the row before (empty for the first) and its hash follows from its
// fields. It returns the id of the first row that breaks the chain, or 0.
func VerifyChain(rows []Row, prev string) (brokenID int64, last string) {
	for _, r := range rows {
		if r.PrevHash != prev {
			return r.ID, prev
		}
		h, err := Hash(prev, r)
		if err != nil || h != r.Hash {
			return r.ID, prev
		}
		prev = r.Hash
	}
	return 0, prev
}

// Verify reads the whole chain in id order and checks it. ok is false with
// the first broken row's id.
func Verify(ctx context.Context, q db.Querier) (ok bool, brokenID int64, err error) {
	const batch = 1000
	var after int64
	prev := ""
	for {
		rows, err := q.Query(ctx, `SELECT `+rowCols+` FROM audit_log l LEFT JOIN accounts a ON a.id = l.actor_account_id
			WHERE l.id > $1 ORDER BY l.id LIMIT $2`, after, batch)
		if err != nil {
			return false, 0, err
		}
		var chunk []Row
		for rows.Next() {
			r, err := scanRow(rows)
			if err != nil {
				rows.Close()
				return false, 0, err
			}
			chunk = append(chunk, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return false, 0, err
		}
		if len(chunk) == 0 {
			return true, 0, nil
		}
		var broken int64
		if broken, prev = VerifyChain(chunk, prev); broken != 0 {
			return false, broken, nil
		}
		after = chunk[len(chunk)-1].ID
	}
}

// Filter narrows a listing.
type Filter struct {
	Action     string
	TargetType string
	TargetID   string
	Limit      int
	Offset     int
}

// List returns one page, newest first, and the total.
func List(ctx context.Context, q db.Querier, f Filter) ([]Row, int, error) {
	where := `WHERE ($1 = '' OR l.action = $1) AND ($2 = '' OR l.target_type = $2) AND ($3 = '' OR l.target_id = $3)`
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM audit_log l `+where, f.Action, f.TargetType, f.TargetID).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := q.Query(ctx, `SELECT `+rowCols+` FROM audit_log l LEFT JOIN accounts a ON a.id = l.actor_account_id `+
		where+` ORDER BY l.id DESC LIMIT $4 OFFSET $5`, f.Action, f.TargetType, f.TargetID, limit, f.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}
