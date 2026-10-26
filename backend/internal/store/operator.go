package store

import (
	"context"
	"encoding/json"
)

// OperatorAccountName is the display name of the account the operator
// command acts as.
const OperatorAccountName = "Operator (command line)"

// OperatorAccount returns the id of the operator account, creating it on
// first use (migration 0003: at most one, no identity, nobody signs in as
// it).
func OperatorAccount(ctx context.Context, q Q) (int64, error) {
	if _, err := q.Exec(ctx, `
		INSERT INTO accounts (display_name, operator) VALUES ($1, true)
		ON CONFLICT (operator) WHERE operator DO NOTHING`, OperatorAccountName); err != nil {
		return 0, err
	}
	var id int64
	err := q.QueryRow(ctx, `SELECT id FROM accounts WHERE operator`).Scan(&id)
	return id, err
}

// SetOperatorListing keeps an operator import's listing with its submission.
func SetOperatorListing(ctx context.Context, q Q, submissionID int64, listing json.RawMessage) error {
	_, err := q.Exec(ctx, `UPDATE submissions SET operator_listing = $2 WHERE id = $1`, submissionID, []byte(listing))
	return err
}

// OperatorListing returns an operator import's listing, nil for a submission
// made through the web interface (ErrNotFound when there is no such
// submission).
func OperatorListing(ctx context.Context, q Q, submissionID int64) (json.RawMessage, error) {
	var b []byte
	err := q.QueryRow(ctx, `SELECT operator_listing FROM submissions WHERE id = $1`, submissionID).Scan(&b)
	if err != nil {
		return nil, notFound(err)
	}
	if len(b) == 0 {
		return nil, nil
	}
	return json.RawMessage(b), nil
}
