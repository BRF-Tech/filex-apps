package store

import (
	"context"
	"time"
)

// IndexSnapshot is the committed catalog state an index is built from.
type IndexSnapshot struct {
	Keys       []SigningKey
	Publishers []Publisher
	Apps       []IndexApp
}

// IndexApp is an app with everything the index shows of it.
type IndexApp struct {
	App
	IconType    string
	Screenshots []AppMedia
	Versions    []Version
}

// LoadIndexSnapshot reads every app with at least one approved version (in
// any status: unlisted apps keep updating installed copies, revoked ones
// tell them to remove it), their versions and media, the publishers they
// name and the published keys. Run it in the publisher's transaction.
func LoadIndexSnapshot(ctx context.Context, q Q, retiredSince time.Time) (IndexSnapshot, error) {
	var s IndexSnapshot
	var err error
	keys, err := PublishedKeys(ctx, q, retiredSince)
	if err != nil {
		return s, err
	}
	// The index lists the keys that sign it and its artifacts; the license
	// key (migration 0006) is in /v1/keys.json only, so adding it changed
	// no index.
	for _, k := range keys {
		if k.Use == "index" || k.Use == "artifact" {
			s.Keys = append(s.Keys, k)
		}
	}

	rows, err := q.Query(ctx, `
		SELECT `+appCols+`, COALESCE(m.content_type, '')
		FROM apps a LEFT JOIN media m ON m.sha256 = a.icon_sha256
		WHERE EXISTS (SELECT 1 FROM app_versions v WHERE v.app_id = a.id)
		ORDER BY a.name`)
	if err != nil {
		return s, err
	}
	byID := map[int64]int{}
	pubs := map[string]bool{}
	for rows.Next() {
		var ia IndexApp
		app, err := scanApp(rows, &ia.IconType)
		if err != nil {
			rows.Close()
			return s, err
		}
		ia.App = app
		ia.Screenshots = []AppMedia{}
		byID[app.ID] = len(s.Apps)
		pubs[app.PublisherID] = true
		s.Apps = append(s.Apps, ia)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return s, err
	}

	vrows, err := q.Query(ctx, `SELECT `+versionCols+` FROM app_versions v ORDER BY v.app_id, v.id`)
	if err != nil {
		return s, err
	}
	for vrows.Next() {
		v, err := scanVersion(vrows)
		if err != nil {
			vrows.Close()
			return s, err
		}
		if i, ok := byID[v.AppID]; ok {
			s.Apps[i].Versions = append(s.Apps[i].Versions, v)
		}
	}
	vrows.Close()
	if err := vrows.Err(); err != nil {
		return s, err
	}

	mrows, err := q.Query(ctx, `
		SELECT am.app_id, am.sha256, m.content_type, am.position, am.caption
		FROM app_media am JOIN media m ON m.sha256 = am.sha256 ORDER BY am.app_id, am.position`)
	if err != nil {
		return s, err
	}
	for mrows.Next() {
		var id int64
		var m AppMedia
		if err := mrows.Scan(&id, &m.SHA256, &m.ContentType, &m.Position, &m.Caption); err != nil {
			mrows.Close()
			return s, err
		}
		if i, ok := byID[id]; ok {
			s.Apps[i].Screenshots = append(s.Apps[i].Screenshots, m)
		}
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return s, err
	}

	ids := make([]string, 0, len(pubs))
	for id := range pubs {
		ids = append(ids, id)
	}
	prows, err := q.Query(ctx, `SELECT `+publisherCols+` FROM publishers p WHERE p.id = ANY($1) ORDER BY p.id`, ids)
	if err != nil {
		return s, err
	}
	defer prows.Close()
	for prows.Next() {
		p, err := scanPublisher(prows)
		if err != nil {
			return s, err
		}
		s.Publishers = append(s.Publishers, p)
	}
	return s, prows.Err()
}
