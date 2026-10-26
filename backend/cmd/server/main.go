// Command server is the filex app store: the API, the signed index, the
// submission workers and the embedded web interface, in one process.
//
// Start-up: the HTTP server comes up at once (/healthz answers, /readyz says
// what is missing, /api/ /v1/ /auth/ answer 503 not_ready); in the
// background the server connects to the database, applies the pending
// migrations and loads the signing keys, then switches to the full handler
// and starts the workers. A failing migration keeps /api/ refused.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/auth/adminrepo"
	"github.com/brf-tech/filex-apps/backend/internal/auth/github"
	"github.com/brf-tech/filex-apps/backend/internal/config"
	"github.com/brf-tech/filex-apps/backend/internal/datakey"
	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/httpapi"
	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/install"
	"github.com/brf-tech/filex-apps/backend/internal/licensing"
	"github.com/brf-tech/filex-apps/backend/internal/media"
	"github.com/brf-tech/filex-apps/backend/internal/notify"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/ratelimit"
	"github.com/brf-tech/filex-apps/backend/internal/resolve"
	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
	"github.com/brf-tech/filex-apps/backend/internal/session"
	"github.com/brf-tech/filex-apps/backend/internal/store"
	"github.com/brf-tech/filex-apps/backend/migrations"
)

func main() {
	// `fapps-server healthcheck`: the image's HEALTHCHECK (the runtime image
	// has no shell, curl or wget).
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	// `fapps-server publisher|import|conversions|publish-index|keygen ...`:
	// the operator commands (operator.go, keygen.go), run inside the
	// container with its environment.
	if len(os.Args) > 1 && isOperatorCommand(os.Args[1]) {
		os.Exit(operator(os.Args[1], os.Args[2:], os.Stdout, os.Stderr))
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration", "err", err)
		os.Exit(2)
	}
	if missing := cfg.Validate(); len(missing) > 0 {
		log.Warn("configuration incomplete", "missing", missing)
	}
	if len(cfg.Removed) > 0 {
		log.Warn("removed settings are still set and ignored: the Keycloak sign-in is gone, roles come from FAPPS_ADMIN_REPO and FAPPS_ADMINS",
			"vars", cfg.Removed)
	}
	fetch, err := openFetch(cfg)
	if err != nil {
		log.Error("outbound client", "err", err)
		os.Exit(2)
	}
	nparts, err := openNotify(cfg)
	if err != nil {
		log.Error("notifications", "err", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var current atomic.Pointer[http.Handler]
	setHandler := func(h http.Handler) { current.Store(&h) }
	setHandler(httpapi.New(httpapi.Deps{Config: cfg, Log: log, NotReady: "The server is starting."}))

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { (*current.Load()).ServeHTTP(w, r) }),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	go func() {
		log.Info("listening", "addr", cfg.Listen, "version", httpapi.Version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	var wg sync.WaitGroup
	var mu sync.Mutex
	var cleanup func()
	wg.Add(1)
	go func() {
		defer wg.Done()
		a, err := boot(ctx, cfg, fetch, nparts, log, func(why string) {
			setHandler(httpapi.New(httpapi.Deps{Config: cfg, Log: log, NotReady: why}))
		})
		if err != nil {
			return
		}
		setHandler(a.handler)
		mu.Lock()
		cleanup = a.close
		mu.Unlock()
		log.Info("ready")
		wg.Add(2)
		go func() {
			defer wg.Done()
			a.notifier.Run(ctx)
		}()
		go func() {
			defer wg.Done()
			a.watchFilex(ctx)
		}()
		a.runner.Run(ctx)
	}()

	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Error("shutdown", "err", err)
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if cleanup != nil {
		cleanup()
	}
}

type app struct {
	handler  http.Handler
	runner   *pipeline.Runner
	notifier *notify.Worker
	// watchFilex announces a newer filex release the store knows of.
	watchFilex func(ctx context.Context)
	close      func()
}

// boot connects, migrates and wires everything, retrying the database
// until ctx ends. notReady reports why the server is not ready yet.
func boot(ctx context.Context, cfg *config.Config, fetch *safehttp.Client, nparts notifyParts, log *slog.Logger, notReady func(string)) (*app, error) {
	if cfg.DatabaseURL == "" {
		notReady("DATABASE_URL is not set.")
		log.Error("DATABASE_URL is not set; serving health only")
		<-ctx.Done()
		return nil, ctx.Err()
	}
	var pool *pgxpool.Pool
	for {
		var err error
		pool, err = connectAndMigrate(ctx, cfg, log)
		if err == nil {
			break
		}
		notReady("The database is not ready: " + err.Error() + ".")
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}

	backfillFileTypes(ctx, pool, log)

	keys, err := index.LoadKeys(ctx, pool, cfg.Keys, cfg.Env, log)
	if err != nil {
		log.Error("signing keys: nothing is signed until this is fixed", "err", err)
		keys = &index.Keyring{}
	}

	var sessions *session.Manager
	secret, err := sessionSecret(cfg, log)
	if err != nil {
		log.Error("session key: sign-in is off", "err", err)
	} else if secret != nil {
		sessions, err = session.NewManager(pool, secret, strings.HasPrefix(cfg.PublicURL, "https://"))
		if err != nil {
			log.Error("session key: sign-in is off", "err", err)
			sessions = nil
		}
	}

	limiter, closeLimiter, err := ratelimit.New(cfg.RedisURL, log)
	if err != nil {
		log.Error("REDIS_URL: counting rate limits in process", "err", err)
		limiter, closeLimiter = ratelimit.NewMemory(), func() error { return nil }
	}

	gh := github.New(cfg.GitHub.ClientID, cfg.GitHub.ClientSecret, cfg.GitHub.OAuthURL, cfg.GitHub.APIURL,
		cfg.PublicURL+"/auth/github/callback")
	roles := adminrepo.Open(adminrepo.Options{GitHub: gh, Repo: cfg.Admin.Repo, Token: cfg.Admin.Token,
		Admins: cfg.Admin.Admins, Reviewers: cfg.Admin.Reviewers, Log: log})
	switch {
	case cfg.Admin.Repo == "" && len(cfg.Admin.Admins)+len(cfg.Admin.Reviewers) == 0:
		log.Warn("roles: neither FAPPS_ADMIN_REPO nor FAPPS_ADMINS is set; nobody gets a store role (reviewer, administrator)")
	case cfg.Admin.Repo == "":
		if cfg.Admin.Token != "" {
			log.Warn("roles: FAPPS_ADMIN_TOKEN is set but FAPPS_ADMIN_REPO is not; the token is not used")
		}
		log.Info("roles: from the lists alone", "admins", len(cfg.Admin.Admins), "reviewers", len(cfg.Admin.Reviewers))
	case cfg.Admin.Token == "":
		log.Warn("roles: FAPPS_ADMIN_TOKEN is not set; the administration repository grants no store role (reviewer, administrator)",
			"repo", cfg.Admin.Repo, "admins", len(cfg.Admin.Admins), "reviewers", len(cfg.Admin.Reviewers))
	default:
		// Ask once at start so /readyz and the admin health know before the
		// first sign-in; a failing answer (GitHub or the network not there
		// yet) is asked again a few times, a 404 (the token does not see the
		// repository) is final.
		go func() {
			for attempt := 1; ; attempt++ {
				pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				h := roles.Probe(pctx)
				cancel()
				log.Info("roles: administration repository", "repo", h.Repo, "status", h.Status, "attempt", attempt)
				if h.Status != adminrepo.StatusFailing || attempt == 6 {
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
			}
		}()
	}

	pub := &index.Releaser{Pool: pool, Keys: keys, PublicURL: cfg.PublicURL, TTL: cfg.Index.TTL,
		RefreshBefore: cfg.Index.RefreshBefore, Log: log}
	if keys.Index != nil {
		if _, err := pub.EnsureFresh(ctx); err != nil {
			log.Error("first index", "err", err)
		}
	} else {
		log.Warn("no index key: /v1/index.json answers index_not_ready")
	}

	sink := &media.Sink{DB: pool, MaxBytes: cfg.Fetch.MaxMedia}
	p := newPorts(cfg, fetch, sink, log)
	runner := &pipeline.Runner{Pool: pool, Resolver: p.resolver, DryRunner: p.runner, Index: pub, Log: log,
		Validators: len(cfg.Validators), StorageValidators: len(cfg.PluginValidators),
		Permissions: pipeline.PermissionRule{Normalize: resolve.NormalizePermissions, IsDerived: resolve.IsDerivedPermission}}
	if p.plugins != nil {
		// Set only when there is one: a nil *plugincheck.Runner in the
		// interface would not read as "none configured".
		runner.StorageDryRunner = p.plugins
	}
	svc := &pipeline.Service{Pool: pool, Repos: p.resolver, Keys: keys, Index: pub, Kick: runner.Kick, Log: log,
		Profiles: newProfiles(cfg, fetch, sink, log)}

	data := dataKeys(cfg, log)
	if keys.License == nil {
		log.Warn("no license signing key (FAPPS_LICENSE_KEY): license checks answer 503")
	}
	lic := &licensing.Service{Pool: pool, Keys: keys, Data: data, Log: log}
	ins := &install.Service{Pool: pool, Keys: keys, Data: data, StoreOrigin: originOf(cfg.PublicURL),
		Development: cfg.Env == "development", Log: log}

	notifier := newNotifier(cfg, pool, data, nparts, log)

	deps := httpapi.Deps{
		Config: cfg, Pool: pool, Sessions: sessions, GitHub: gh, Roles: roles, Limiter: limiter, Index: pub,
		Service: svc, Validators: p.runner, Licensing: lic, Install: ins, Notify: notifier, Log: log,
	}
	if p.plugins != nil {
		deps.PluginValidators = p.plugins
	}
	h := httpapi.New(deps)
	return &app{handler: h, runner: runner, notifier: newNotifyWorker(notifier, roles, log),
		watchFilex: func(ctx context.Context) { watchFilexReleases(ctx, notifier, p.runner, p.plugins, log) }, close: func() {
			_ = closeLimiter()
			pool.Close()
		}}, nil
}

func connectAndMigrate(ctx context.Context, cfg *config.Config, log *slog.Logger) (*pgxpool.Pool, error) {
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Warn("database", "err", err)
		return nil, errors.New("unreachable")
	}
	mctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	applied, err := db.Migrate(mctx, pool, migrations.FS)
	if err != nil {
		pool.Close()
		log.Error("migrations failed; /api/ stays refused", "err", err)
		return nil, errors.New("migrations failed")
	}
	if len(applied) > 0 {
		log.Info("migrations applied", "versions", applied)
	}
	return pool, nil
}

// backfillFileTypes extracts the file types of the versions approved before
// migration 0007 (or by an older extractor) from the manifests they keep, so
// the catalog's file-type search finds them. Best effort: a failure is
// logged and tried again at the next start; the store serves either way.
func backfillFileTypes(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) {
	bctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	res, err := store.BackfillFileTypes(bctx, pool)
	switch {
	case err != nil:
		log.Error("file types: backfill", "err", err, "extracted", res.Extracted)
	case len(res.Failed) > 0:
		log.Error("file types: manifests not read", "versions", res.Failed, "extracted", res.Extracted)
	case res.Extracted > 0:
		log.Info("file types: backfill", "extracted", res.Extracted)
	}
}

// sessionSecret reads FAPPS_SESSION_KEY; in development without one a
// random key lives as long as the process (sessions end at restart).
func sessionSecret(cfg *config.Config, log *slog.Logger) ([]byte, error) {
	if cfg.SessionKey != "" {
		return session.ParseSecret(cfg.SessionKey)
	}
	if cfg.Env == "development" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		log.Warn("FAPPS_SESSION_KEY not set: a random key for this process (development only)")
		return b, nil
	}
	log.Warn("FAPPS_SESSION_KEY not set: sign-in is off, the store is read-only")
	return nil, nil
}

// dataKeys derives the commercial data's keys from FAPPS_DATA_KEY; in
// development without one a random key lives as long as the process
// (license keys and saved instances made with it stop matching at restart).
// Without it elsewhere, licenses, install links and saved instances answer
// 503.
func dataKeys(cfg *config.Config, log *slog.Logger) *datakey.Keys {
	secret := cfg.DataKey
	if secret == nil && cfg.Env == "development" {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			log.Error("FAPPS_DATA_KEY", "err", err)
			return nil
		}
		log.Warn("FAPPS_DATA_KEY not set: a random key for this process (development only)")
	}
	if secret == nil {
		log.Warn("FAPPS_DATA_KEY not set: licenses, install links and saved instances answer 503")
		return nil
	}
	k, err := datakey.New(secret)
	if err != nil {
		log.Error("FAPPS_DATA_KEY: licenses, install links and saved instances are off", "err", err)
		return nil
	}
	return k
}

// originOf is scheme://host[:port] of the store's public URL.
func originOf(public string) string {
	u, err := url.Parse(public)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimRight(public, "/")
	}
	return u.Scheme + "://" + u.Host
}

func healthcheck() int {
	addr := os.Getenv("FAPPS_LISTEN")
	if addr == "" {
		addr = "0.0.0.0:8080"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
