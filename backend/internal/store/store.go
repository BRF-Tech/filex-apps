// Package store holds the queries, one file per aggregate (accounts,
// sessions, publishers, apps, versions, submissions, media, keys, index
// releases, jobs). Every function takes a db.Querier, so it runs alone on
// the pool or inside a caller's transaction; nothing here starts one.
package store

import (
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/brf-tech/filex-apps/backend/internal/db"
)

// Q is a pool or a transaction.
type Q = db.Querier

// ErrNotFound is returned when the row asked for does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a unique rule refuses the write.
var ErrConflict = errors.New("conflict")

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Page is a one-based page of at most PerPage rows.
type Page struct {
	Page    int
	PerPage int
}

// Limit and Offset for SQL.
func (p Page) Limit() int {
	if p.PerPage <= 0 {
		return 50
	}
	return p.PerPage
}

// Offset for SQL.
func (p Page) Offset() int {
	if p.Page <= 1 {
		return 0
	}
	return (p.Page - 1) * p.Limit()
}

// likePattern turns a word into an ILIKE pattern that matches it anywhere,
// with the pattern characters escaped.
func likePattern(word string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(word) + "%"
}

func emptyIfNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func isUnique(err error) bool {
	_, ok := db.UniqueViolation(err)
	return ok
}

func itoa(n int) string { return strconv.Itoa(n) }
