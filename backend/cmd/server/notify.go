package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/auth/adminrepo"
	"github.com/brf-tech/filex-apps/backend/internal/config"
	"github.com/brf-tech/filex-apps/backend/internal/datakey"
	"github.com/brf-tech/filex-apps/backend/internal/filetypes"
	"github.com/brf-tech/filex-apps/backend/internal/httpapi"
	"github.com/brf-tech/filex-apps/backend/internal/notify"
	"github.com/brf-tech/filex-apps/backend/internal/plugincheck"
	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
	"github.com/brf-tech/filex-apps/backend/internal/validator"
)

// notifyParts are what the notifications need before the database is
// there: the Web Push key pair and the guarded client of the webhooks and
// push services. A malformed key pair, or a test mode outside development,
// stops the server.
type notifyParts struct {
	vapid  *notify.VAPID
	client *safehttp.Client
}

func openNotify(cfg *config.Config) (notifyParts, error) {
	var out notifyParts
	if cfg.Push.PublicKey != "" {
		v, err := notify.ParseVAPID(cfg.Push.PublicKey, cfg.Push.PrivateKey, cfg.Push.Subject)
		if err != nil {
			return out, err
		}
		out.vapid = v
	}
	// Any host (a person's webhook, a browser's push service), but HTTPS and
	// public addresses only, checked after DNS on every hop. Development
	// also takes plain http and, with FAPPS_FETCH_ALLOW_PRIVATE=1, a
	// receiver on the developer's own machine.
	c, err := safehttp.Open(safehttp.Config{
		AnyHost:      true,
		Env:          cfg.Env,
		CAFile:       cfg.Fetch.CAFile,
		AllowPrivate: cfg.Fetch.AllowPrivate,
		AllowHTTP:    cfg.Env == safehttp.DevelopmentEnv,
		Timeout:      notify.SendTimeout,
		MaxRedirects: 3,
		UserAgent:    "filex-apps-notify/" + httpapi.Version,
	})
	if err != nil {
		return out, err
	}
	out.client = c
	return out, nil
}

// newNotifier is the notification service on the database.
func newNotifier(cfg *config.Config, pool *pgxpool.Pool, data *datakey.Keys, parts notifyParts, log *slog.Logger) *notify.Service {
	if parts.vapid == nil {
		log.Warn("FAPPS_VAPID_PUBLIC_KEY / FAPPS_VAPID_PRIVATE_KEY not set: browser notifications are off (webhooks work)")
	}
	return &notify.Service{Pool: pool, Data: data, VAPID: parts.vapid,
		Sender:    &notify.Sender{Client: parts.client, UserAgent: "filex-apps-notify/" + httpapi.Version + " (+" + cfg.PublicURL + ")"},
		PublicURL: cfg.PublicURL, StoreName: cfg.Brand.Name, Development: cfg.Env == safehttp.DevelopmentEnv, Log: log}
}

// newNotifyWorker works the notifier's jobs; the administrators' events ask
// the role sources again (internal/auth/adminrepo).
func newNotifyWorker(svc *notify.Service, roles *adminrepo.Checker, log *slog.Logger) *notify.Worker {
	return &notify.Worker{Svc: svc, Admins: &notify.RepoAdmins{Pool: svc.Pool, Roles: roles}, Log: log}
}

// watchFilexReleases announces a newer filex release the store knows of: at
// start (once the validators had a minute to come up) and every ten
// minutes, from the filex-builtin.json the image carries and the versions
// its validators report. The store never asks GitHub (notify.NoteFilexVersions).
func watchFilexReleases(ctx context.Context, svc *notify.Service, vals *validator.Runner, plugins *plugincheck.Runner, log *slog.Logger) {
	check := func() {
		reports := []string{}
		if b := filetypes.Builtin(); b != nil {
			reports = append(reports, b.Filex)
		}
		if vals != nil {
			pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			for _, s := range vals.Statuses(pctx) {
				if s.Reachable {
					reports = append(reports, s.FilexVersion)
				}
			}
			cancel()
		}
		if plugins != nil {
			for _, s := range plugins.Statuses() {
				if s.Reachable {
					reports = append(reports, s.FilexVersion)
				}
			}
		}
		v, err := svc.NoteFilexVersions(ctx, reports)
		switch {
		case err != nil && ctx.Err() == nil:
			log.Error("notify: filex releases", "err", err)
		case v != "":
			log.Info("notify: a newer filex release", "version", v)
		}
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Minute):
	}
	check()
	tick := time.NewTicker(10 * time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			check()
		}
	}
}
