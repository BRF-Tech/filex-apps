// Package httpapi is the HTTP surface: every route of api/openapi.yaml, the
// middleware chain (security headers, request id and log, panic recovery,
// rate limits, sessions, origin and CSRF checks) and JSON errors.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/auth/adminrepo"
	"github.com/brf-tech/filex-apps/backend/internal/auth/github"
	"github.com/brf-tech/filex-apps/backend/internal/config"
	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/install"
	"github.com/brf-tech/filex-apps/backend/internal/licensing"
	"github.com/brf-tech/filex-apps/backend/internal/notify"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/plugincheck"
	"github.com/brf-tech/filex-apps/backend/internal/ratelimit"
	"github.com/brf-tech/filex-apps/backend/internal/session"
	"github.com/brf-tech/filex-apps/backend/internal/store"
	"github.com/brf-tech/filex-apps/backend/internal/validator"
	"github.com/brf-tech/filex-apps/backend/internal/webui"
)

// Version is stamped at build time (-ldflags "-X .../httpapi.Version=...").
var Version = "dev"

// ValidatorProber reports every configured validator's health and filex
// version (internal/validator's Runner).
type ValidatorProber interface {
	Statuses(ctx context.Context) []validator.Status
}

// PluginValidatorProber reports every plugin validator's heartbeat
// (internal/plugincheck's Runner).
type PluginValidatorProber interface {
	Statuses() []plugincheck.Health
}

// Deps is what the handlers use. With Pool nil the server is not ready:
// only /healthz, /readyz and the web interface answer, everything under
// /api/, /v1/ and /auth/ answers 503 not_ready.
type Deps struct {
	Config     *config.Config
	Pool       *pgxpool.Pool
	Sessions   *session.Manager   // nil: sign-in is off (no session key)
	GitHub     *github.Provider   // the sign-in, for everyone
	Roles      *adminrepo.Checker // the store roles (nil: nobody gets one)
	Limiter    ratelimit.Limiter  // nil: no rate limits (tests)
	Index      *index.Releaser
	Service    *pipeline.Service
	Validators ValidatorProber
	// PluginValidators are the storage plugins' validators (nil: none is
	// configured).
	PluginValidators PluginValidatorProber
	// Licensing and Install are paid apps and install links (nil: those
	// routes answer 503 data_key_missing).
	Licensing *licensing.Service
	Install   *install.Service
	// Notify is the notification channels (nil: those routes answer 503).
	Notify *notify.Service
	Log    *slog.Logger
	// NotReady explains why the server is not ready yet (Pool nil).
	NotReady string
}

type handler struct {
	Deps
	cfg      *config.Config
	log      *slog.Logger
	sessions *session.Manager
	limiter  ratelimit.Limiter
	origin   string
	started  time.Time
}

// New builds the server's handler.
func New(d Deps) http.Handler {
	if d.Config == nil {
		d.Config = &config.Config{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	h := &handler{Deps: d, cfg: d.Config, log: d.Log, sessions: d.Sessions, limiter: d.Limiter,
		origin: originOf(d.Config.PublicURL), started: time.Now().UTC()}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": Version, "started_at": h.started.Format(time.RFC3339)})
	})
	mux.HandleFunc("GET /readyz", h.readyz)
	h.storeRoutes(mux)

	if d.Pool == nil {
		notReady := func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/v1/index.json") {
				writeError(w, http.StatusServiceUnavailable, "index_not_ready", "No signed index has been generated yet.")
				return
			}
			writeError(w, http.StatusServiceUnavailable, "not_ready", orStr(d.NotReady, "The database is not ready."))
		}
		mux.HandleFunc("/api/", notReady)
		mux.HandleFunc("/v1/", notReady)
		mux.HandleFunc("/auth/", notReady)
	} else {
		h.routes(mux)
	}
	mux.Handle("/", webui.Handler(webui.Options{Name: d.Config.Brand.Name}))
	return h.securityHeaders(h.requestLog(mux))
}

// route registers a path with its methods; any other method answers a JSON
// 405 with Allow.
func route(mux *http.ServeMux, path string, methods map[string]http.HandlerFunc) {
	var allow []string
	for meth, fn := range methods {
		mux.HandleFunc(meth+" "+path, fn)
		allow = append(allow, meth)
		if meth == http.MethodGet {
			allow = append(allow, http.MethodHead)
		}
	}
	sort.Strings(allow)
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", strings.Join(allow, ", "))
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "This endpoint does not take "+r.Method+".")
	})
}

type methods = map[string]http.HandlerFunc

func (h *handler) routes(mux *http.ServeMux) {
	// index: the filex client contract (public, uncapped, cacheable)
	route(mux, "/v1/index.json", methods{"GET": h.getIndex})
	route(mux, "/v1/index.json.sig", methods{"GET": h.getIndexSig})
	route(mux, "/v1/keys.json", methods{"GET": h.getKeys})
	route(mux, "/v1/media/{file}", methods{"GET": h.getMedia})
	route(mux, "/v1/releases", methods{"GET": h.listReleases})
	route(mux, "/v1/releases/{serial}/index.json", methods{"GET": h.getReleaseBody})
	route(mux, "/v1/releases/{serial}/index.json.sig", methods{"GET": h.getReleaseSig})
	// license checks and install links: what a filex SERVER calls (no
	// session; their own rate limits)
	route(mux, "/v1/licenses/verify", methods{"POST": h.verifyLicense})
	route(mux, "/v1/install/{token}", methods{"GET": h.readIntent})
	route(mux, "/v1/install/{token}/complete", methods{"POST": h.completeIntent})
	// instance connections: a filex server spends a connection code, then
	// signs its requests with its own key (docs/ARCHITECTURE.md "Instance
	// connections")
	route(mux, "/v1/instances/connect", methods{"POST": h.connectInstance})
	route(mux, "/v1/instances/{id}/connection", methods{"GET": h.instanceSelf, "DELETE": h.instanceDisconnect})
	route(mux, "/v1/instances/{id}/intents", methods{"POST": h.instanceIntent})

	// catalog
	route(mux, "/api/v1/apps", methods{"GET": h.api(h.listApps)})
	route(mux, "/api/v1/apps/{name}", methods{"GET": h.api(h.getApp)})
	route(mux, "/api/v1/apps/{name}/versions/{version}", methods{"GET": h.api(h.getAppVersion)})
	route(mux, "/api/v1/publishers/{id}", methods{"GET": h.api(h.getPublisher)})
	route(mux, "/api/v1/categories", methods{"GET": h.api(h.listCategories)})

	// auth
	route(mux, "/auth/github/login", methods{"GET": h.githubLogin})
	route(mux, "/auth/github/callback", methods{"GET": h.githubCallback})
	route(mux, "/auth/logout", methods{"POST": h.logout})
	mux.HandleFunc("/auth/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "No such endpoint.")
	})

	// me
	route(mux, "/api/v1/me", methods{"GET": h.api(h.getMe)})
	route(mux, "/api/v1/me/apps", methods{"GET": h.signedIn(h.myApps)})
	route(mux, "/api/v1/me/instances", methods{"GET": h.signedIn(h.listInstances), "POST": h.mutating(h.saveInstance)})
	route(mux, "/api/v1/me/instances/{id}", methods{"DELETE": h.mutating(h.deleteInstance)})
	route(mux, "/api/v1/me/instances/{id}/connect-code", methods{"POST": h.mutating(h.createConnectCode)})
	route(mux, "/api/v1/me/instances/{id}/connection", methods{"DELETE": h.mutating(h.disconnectInstance)})

	// notifications (the caller's own channels)
	route(mux, "/api/v1/me/notifications", methods{"GET": h.signedIn(h.getNotifications)})
	route(mux, "/api/v1/me/notifications/push", methods{"PUT": h.mutating(h.setPushEvents)})
	route(mux, "/api/v1/me/push-subscriptions", methods{"POST": h.mutating(h.subscribePush)})
	route(mux, "/api/v1/me/push-subscriptions/{id}", methods{"DELETE": h.mutating(h.unsubscribePush)})
	route(mux, "/api/v1/me/push-subscriptions/{id}/test", methods{"POST": h.mutating(h.testPush)})
	route(mux, "/api/v1/me/webhooks", methods{"POST": h.mutating(h.createWebhook)})
	route(mux, "/api/v1/me/webhooks/{id}", methods{"PATCH": h.mutating(h.updateWebhook), "DELETE": h.mutating(h.deleteWebhook)})
	route(mux, "/api/v1/me/webhooks/{id}/test", methods{"POST": h.mutating(h.testWebhook)})

	// licensing (a member of the app's publisher, or an administrator)
	route(mux, "/api/v1/apps/{name}/pricing", methods{"PUT": h.mutating(h.setPricing)})
	route(mux, "/api/v1/apps/{name}/licenses", methods{"GET": h.signedIn(h.listLicenses), "POST": h.mutating(h.createLicense)})
	route(mux, "/api/v1/licenses/{id}/revoke", methods{"POST": h.mutating(h.revokeLicense)})
	route(mux, "/api/v1/licenses/{id}/activations", methods{"GET": h.signedIn(h.listActivations)})
	route(mux, "/api/v1/licenses/{id}/activations/{instance_id}/release", methods{"POST": h.mutating(h.releaseActivation)})

	// install links (a signed-in person)
	route(mux, "/api/v1/install-intents", methods{"POST": h.mutating(h.createIntent)})

	// publishing
	route(mux, "/api/v1/publishers", methods{"GET": h.api(h.listPublishers), "POST": h.mutating(h.requestPublisher)})
	route(mux, "/api/v1/submissions", methods{"POST": h.mutating(h.createSubmission), "GET": h.signedIn(h.listSubmissions)})
	route(mux, "/api/v1/submissions/{id}", methods{"GET": h.signedIn(h.getSubmission)})
	route(mux, "/api/v1/submissions/{id}/withdraw", methods{"POST": h.mutating(h.withdrawSubmission)})
	route(mux, "/api/v1/submissions/{id}/retry", methods{"POST": h.mutating(h.retrySubmission)})

	// review
	route(mux, "/api/v1/review/queue", methods{"GET": h.signedIn(role(reviewerRole, h.reviewQueue))})
	route(mux, "/api/v1/review/submissions/{id}/approve", methods{"POST": h.mutating(role(reviewerRole, h.approve))})
	route(mux, "/api/v1/review/submissions/{id}/reject", methods{"POST": h.mutating(role(reviewerRole, h.reject))})
	route(mux, "/api/v1/review/submissions/{id}/comment", methods{"POST": h.mutating(role(reviewerRole, h.comment))})
	route(mux, "/api/v1/review/submissions/{id}/recheck", methods{"POST": h.mutating(role(reviewerRole, h.recheck))})

	// admin
	route(mux, "/api/v1/admin/apps", methods{"GET": h.signedIn(role(reviewerRole, h.adminListApps))})
	route(mux, "/api/v1/admin/apps/{name}", methods{"PATCH": h.mutating(role(adminRole, h.adminUpdateApp))})
	route(mux, "/api/v1/admin/apps/{name}/revoke", methods{
		"POST": h.mutating(role(adminRole, h.revokeApp)), "DELETE": h.mutating(role(adminRole, h.unrevokeApp))})
	route(mux, "/api/v1/admin/apps/{name}/versions/{version}/yank", methods{
		"POST": h.mutating(role(adminRole, h.yankVersion)), "DELETE": h.mutating(role(adminRole, h.unyankVersion))})
	route(mux, "/api/v1/admin/publishers", methods{"GET": h.signedIn(role(adminRole, h.adminListPublishers))})
	route(mux, "/api/v1/admin/publishers/{id}", methods{"PATCH": h.mutating(role(adminRole, h.adminUpdatePublisher))})
	route(mux, "/api/v1/admin/index/publish", methods{"POST": h.mutating(role(adminRole, h.publishIndex))})
	route(mux, "/api/v1/admin/keys", methods{"GET": h.signedIn(role(adminRole, h.adminKeys))})
	route(mux, "/api/v1/admin/health", methods{"GET": h.signedIn(role(adminRole, h.adminHealth))})
	route(mux, "/api/v1/admin/validators", methods{"GET": h.signedIn(role(adminRole, h.adminValidators))})
	route(mux, "/api/v1/admin/audit", methods{"GET": h.signedIn(role(adminRole, h.adminAudit))})

	// Anything under /api/ or /v1/ that no route claimed is a JSON 404, not
	// the SPA's index.html.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "No such endpoint.")
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "No such endpoint.")
	})
}

// readyz: the configuration is complete, the database answers and is
// migrated, and a signed index exists. `warnings` (it stays ready) carries
// the role check's trouble and removed variables that are still set.
func (h *handler) readyz(w http.ResponseWriter, r *http.Request) {
	warnings := h.warnings()
	missing := h.cfg.Validate()
	if h.Pool == nil {
		missing = append(missing, "database: "+orStr(h.NotReady, "not connected or not migrated"))
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := h.Pool.Ping(ctx); err != nil {
			missing = append(missing, "database: unreachable")
		} else if serial, err := store.LatestSerial(ctx, h.Pool); err != nil || serial == 0 {
			missing = append(missing, "signed index")
		}
	}
	body := map[string]any{"status": "ready"}
	status := http.StatusOK
	if len(missing) > 0 {
		body = map[string]any{"status": "not_ready", "missing": missing}
		status = http.StatusServiceUnavailable
	}
	if len(warnings) > 0 {
		body["warnings"] = warnings
	}
	writeJSON(w, status, body)
}

// warnings: what does not stop the store but someone must fix.
func (h *handler) warnings() []string {
	var out []string
	if w := h.Roles.Warning(); w != "" {
		out = append(out, w)
	}
	if h.Licensing != nil && h.Licensing.Data == nil {
		out = append(out, "FAPPS_DATA_KEY is not set: licenses, install links and saved instances answer 503")
	} else if h.Licensing != nil && (h.Licensing.Keys == nil || h.Licensing.Keys.License == nil) {
		out = append(out, "no license signing key (FAPPS_LICENSE_KEY): license checks answer 503")
	}
	if len(h.cfg.Removed) > 0 {
		out = append(out, "removed settings are still set and ignored: "+strings.Join(h.cfg.Removed, ", ")+
			" (the Keycloak sign-in is gone; roles come from FAPPS_ADMIN_REPO and FAPPS_ADMINS)")
	}
	return out
}

func orStr(s, def string) string {
	if s != "" {
		return s
	}
	return def
}
