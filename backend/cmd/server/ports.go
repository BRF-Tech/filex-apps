package main

import (
	"log/slog"

	"github.com/brf-tech/filex-apps/backend/internal/config"
	ghapi "github.com/brf-tech/filex-apps/backend/internal/github"
	"github.com/brf-tech/filex-apps/backend/internal/httpapi"
	"github.com/brf-tech/filex-apps/backend/internal/media"
	"github.com/brf-tech/filex-apps/backend/internal/plugincheck"
	"github.com/brf-tech/filex-apps/backend/internal/resolve"
	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
	"github.com/brf-tech/filex-apps/backend/internal/validator"
)

// openFetch builds the guarded outbound client. Its test modes (an extra
// CA, private addresses) exist for the end-to-end stack's mock GitHub and
// are refused outside APP_ENV=development: the server does not start then.
func openFetch(cfg *config.Config) (*safehttp.Client, error) {
	return safehttp.Open(safehttp.Config{
		Env:          cfg.Env,
		CAFile:       cfg.Fetch.CAFile,
		AllowPrivate: cfg.Fetch.AllowPrivate,
		AllowHosts:   cfg.Fetch.AllowHosts,
		Timeout:      cfg.Fetch.Timeout,
		MaxRedirects: cfg.Fetch.MaxRedirects,
		UserAgent:    "filex-apps-store/" + httpapi.Version,
	})
}

// ports are the validator lane's implementations of the pipeline's seams:
// the resolver (and its synchronous repository check), the dry runner and
// the validators' health.
type ports struct {
	resolver *resolve.Resolver
	runner   *validator.Runner
	// plugins runs a storage plugin's conformance on the plugin validators;
	// nil when FAPPS_PLUGIN_VALIDATORS names none.
	plugins *plugincheck.Runner
}

// newPorts builds the GitHub reader on the guarded client, the resolver
// (its images go to the media sink) and the dry runner.
func newPorts(cfg *config.Config, hc *safehttp.Client, sink *media.Sink, log *slog.Logger) ports {
	if cfg.GitHub.Token == "" {
		log.Warn("FAPPS_GITHUB_TOKEN not set: background GitHub reads are anonymous (60 API calls an hour, about 20 resolutions)")
	}
	gh := ghapi.New(ghapi.Options{HTTP: hc, APIURL: cfg.GitHub.APIURL, RawURL: cfg.GitHub.RawURL, Token: cfg.GitHub.Token})
	res := resolve.New(resolve.Options{GitHub: gh, Media: sink, Limits: resolve.Limits{
		Manifest: cfg.Fetch.MaxManifest, Wasm: cfg.Fetch.MaxWasm, UI: cfg.Fetch.MaxUIBundle, Media: cfg.Fetch.MaxMedia,
		Binary: cfg.Fetch.MaxBinary,
	}})
	vs := make([]validator.Validator, 0, len(cfg.Validators))
	for _, v := range cfg.Validators {
		vs = append(vs, validator.Validator{Label: v.Label, BaseURL: v.BaseURL})
	}
	run := validator.New(validator.Options{Validators: vs, Email: cfg.ValidatorEmail, Password: cfg.ValidatorPassword})
	out := ports{resolver: res, runner: run}
	if len(cfg.PluginValidators) > 0 {
		pvs := make([]plugincheck.Validator, 0, len(cfg.PluginValidators))
		for _, v := range cfg.PluginValidators {
			pvs = append(pvs, plugincheck.Validator{Label: v.Label, Dir: v.Dir})
		}
		out.plugins = plugincheck.New(plugincheck.Options{Validators: pvs, Fetch: plugincheck.GitHubFetcher(gh), MaxBinary: cfg.Fetch.MaxBinary})
	} else {
		log.Warn("FAPPS_PLUGIN_VALIDATORS not set: storage plugins cannot be checked, and their submissions fail the dry run")
	}
	return out
}

// newProfiles reads a publisher's GitHub profile and mirrors its avatar into
// the media sink (pipeline.ProfileReader).
func newProfiles(cfg *config.Config, hc *safehttp.Client, sink *media.Sink, log *slog.Logger) *githubProfiles {
	gh := ghapi.New(ghapi.Options{HTTP: hc, APIURL: cfg.GitHub.APIURL, RawURL: cfg.GitHub.RawURL, Token: cfg.GitHub.Token})
	return &githubProfiles{gh: gh, http: hc, sink: sink, max: cfg.Fetch.MaxMedia, log: log}
}
