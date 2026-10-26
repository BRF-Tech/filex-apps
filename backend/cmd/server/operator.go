package main

// The operator commands (docs/ARCHITECTURE.md "Operator import (official
// publisher)"), run inside the store's own container with its environment:
//
//	fapps-server publisher -id <id> -name <name> -github-owner <login> [-homepage <url>]
//	    makes the publisher bound to the GitHub owner's numeric id (as GitHub
//	    answers it now) active, verified and official, creating it if needed.
//	fapps-server import -publisher <id> -repo <owner/name> -ref <tag> -listing <file> [-approve] [-note <text>] [-timeout 30m]
//	    queues an operator import: the listing file has the .filex-apps.json
//	    schema, its paths (description, icon, screenshots) are files next to
//	    it. The server's workers run the whole pipeline (resolve, policy,
//	    artifacts, media, the dry runs on every validator, the pins); the
//	    command waits, prints every check and, with -approve, approves a
//	    submission that reached review (which signs the version and publishes
//	    the index).
//	fapps-server conversions -app <name> -version <version> -listing <file>
//	    sets the conversions an approved version of an official publisher's
//	    app declares to the listing file's `conversions` (the
//	    .filex-apps.json schema; none removes them), after the same checks
//	    as at approval: the schema, the listing's publisher is the app's, and
//	    every engine a conversion needs is one the version's manifest asks
//	    for. Audited (version.conversions); no index is published, the index
//	    does not carry conversions.
//	fapps-server publish-index
//	    re-signs the index from the committed state (serial + 1, a new
//	    expiry), as the administration page's "Publish now" does. For a
//	    release that changes what the index says about versions already
//	    listed (their permissions, 2026-10-16): nothing else publishes it until the
//	    next approval or the refresh before expiry. Audited (index.publish).
//	fapps-server notify-webhook -login <github login> -name <name> -format generic|slack|discord|teams -url <url> [-header-name <name>] [-events all|<kind>,...] [-test]
//	    adds a webhook to the account that signs in as that GitHub login, as
//	    its Notifications page does. With -header-name the header's value is
//	    the first line of standard input (a secret never goes on a command
//	    line). -events all: every event of the store; the administrators'
//	    events still reach that account only while GitHub says it is an
//	    administrator. A generic webhook's signing secret is not printed
//	    (the page shows a new one when the webhook is added there). -test
//	    sends a test at once and prints the answer. Audited (webhook.create,
//	    by the operator for the account).
//	fapps-server notify-test -login <github login>
//	    sends a test to every webhook of that account and prints each answer.
//	fapps-server keygen -out <file> [-suffix <YYYY-MM>] [-store <https address>]
//	    makes a new store's keys and secrets into a new file (mode 0600) and
//	    prints only the public keys, their fingerprints and a filex's
//	    settings (keygen.go). Needs no database and no configuration.
//
// Only an official, verified publisher bound to the repository's owner can
// be imported this way; everything else goes through the web interface.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/config"
	"github.com/brf-tech/filex-apps/backend/internal/consent"
	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	ghapi "github.com/brf-tech/filex-apps/backend/internal/github"
	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/media"
	"github.com/brf-tech/filex-apps/backend/internal/notify"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/resolve"
	"github.com/brf-tech/filex-apps/backend/internal/store"
	"github.com/brf-tech/filex-apps/backend/migrations"
)

const operatorUsage = `usage:
  fapps-server publisher -id <id> -name <name> -github-owner <login> [-homepage <url>]
  fapps-server import -publisher <id> -repo <owner/name> -ref <tag> -listing <file> [-kind app|storage] [-approve] [-note <text>] [-timeout 30m]
  fapps-server conversions -app <name> -version <version> -listing <file>
  fapps-server publish-index
  fapps-server notify-webhook -login <github login> -name <name> -format generic|slack|discord|teams -url <url> [-header-name <name>] [-events all|<kind>,...] [-test]
  fapps-server notify-test -login <github login>
  fapps-server keygen -out <file> [-suffix <YYYY-MM>] [-store <https address>]
`

// isOperatorCommand says whether the binary's first argument names an
// operator command. Anything else starts the server, so a command missing
// here would open a second store in the running container.
func isOperatorCommand(cmd string) bool {
	switch cmd {
	case "publisher", "import", "conversions", "publish-index", "notify-webhook", "notify-test", "keygen":
		return true
	}
	return false
}

// stdin is where an operator command reads a secret from (tests replace it).
var stdin io.Reader = os.Stdin

// operator runs one operator command and returns the exit code.
func operator(cmd string, args []string, stdout, stderr io.Writer) int {
	if cmd == "keygen" {
		// No store to open: it makes the keys a store starts with.
		return keygen(args, stdout, stderr, time.Now())
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var run func(ctx context.Context, o *ops) error
	var required []*string
	switch cmd {
	case "publisher":
		id := fs.String("id", "", "publisher id")
		name := fs.String("name", "", "display name")
		owner := fs.String("github-owner", "", "GitHub user or organization login")
		homepage := fs.String("homepage", "", "homepage address")
		required = []*string{id, name, owner}
		run = func(ctx context.Context, o *ops) error {
			return o.publisher(ctx, *id, *name, *owner, *homepage, stdout)
		}
	case "import":
		pubID := fs.String("publisher", "", "publisher id")
		repo := fs.String("repo", "", "owner/name")
		ref := fs.String("ref", "", "tag")
		listing := fs.String("listing", "", "listing file (.filex-apps.json schema)")
		note := fs.String("note", "", "note kept with the submission")
		kind := fs.String("kind", "app", "app (an app or a language pack) or storage (a storage plugin)")
		approve := fs.Bool("approve", false, "approve when every check passed")
		timeout := fs.Duration("timeout", 30*time.Minute, "how long to wait for the pipeline")
		required = []*string{pubID, repo, ref, listing}
		run = func(ctx context.Context, o *ops) error {
			l, err := loadListing(*listing, o.cfg.Fetch.MaxMedia)
			if err != nil {
				return err
			}
			if !domain.ValidSubmissionKind(*kind) {
				return fmt.Errorf("-kind must be app or storage, not %q", *kind)
			}
			return o.importOne(ctx, pipeline.ImportRequest{Publisher: *pubID, Repo: *repo, Ref: *ref, Note: *note, Listing: l, Kind: *kind}, *approve, *timeout, stdout)
		}
	case "conversions":
		app := fs.String("app", "", "app name")
		version := fs.String("version", "", "an approved version")
		listing := fs.String("listing", "", "listing file (.filex-apps.json schema)")
		required = []*string{app, version, listing}
		run = func(ctx context.Context, o *ops) error {
			l, err := loadConversions(*listing)
			if err != nil {
				return err
			}
			return o.conversions(ctx, *app, *version, l, stdout)
		}
	case "publish-index":
		run = func(ctx context.Context, o *ops) error {
			return o.publishIndex(ctx, stdout)
		}
	case "notify-webhook":
		login := fs.String("login", "", "the GitHub login of the account the webhook is for")
		name := fs.String("name", "", "the webhook's name")
		format := fs.String("format", "generic", "generic, slack, discord or teams")
		addr := fs.String("url", "", "the webhook's address (https)")
		header := fs.String("header-name", "", "a header of the receiver's (generic only); its value is read from standard input")
		events := fs.String("events", "all", "all, or a comma list of event kinds")
		test := fs.Bool("test", false, "send a test at once")
		required = []*string{login, name, format, addr}
		run = func(ctx context.Context, o *ops) error {
			value := ""
			if *header != "" {
				line, err := readSecretLine(stdin)
				if err != nil {
					return fmt.Errorf("-header-name: the value is the first line of standard input: %w", err)
				}
				value = line
			}
			return o.notifyWebhook(ctx, *login, notify.WebhookCreate{Name: *name, Format: *format, URL: *addr,
				HeaderName: *header, HeaderValue: value, Events: eventList(*events)}, *test, stdout)
		}
	case "notify-test":
		login := fs.String("login", "", "the GitHub login of the account")
		required = []*string{login}
		run = func(ctx context.Context, o *ops) error {
			return o.notifyTest(ctx, *login, stdout)
		}
	default:
		fmt.Fprintf(stderr, "fapps-server: unknown command %q\n%s", cmd, operatorUsage)
		return 2
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintf(stderr, "fapps-server %s: bad arguments\n%s", cmd, operatorUsage)
		return 2
	}
	for _, v := range required {
		if strings.TrimSpace(*v) == "" {
			fmt.Fprintf(stderr, "fapps-server %s: a required flag is missing\n%s", cmd, operatorUsage)
			return 2
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	o, err := openOps(ctx, stderr)
	if err == nil {
		defer o.close()
		err = run(ctx, o)
	}
	if err != nil {
		fmt.Fprintf(stderr, "fapps-server %s: %v\n", cmd, err)
		return 1
	}
	return 0
}

// ops is the store wired for the operator commands: the same database,
// keys, resolver and validators as the server.
type ops struct {
	cfg    *config.Config
	pool   *pgxpool.Pool
	svc    *pipeline.Service
	gh     *ghapi.Client
	notify *notify.Service
	log    *slog.Logger
}

func openOps(ctx context.Context, stderr io.Writer) (*ops, error) {
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("configuration: %w", err)
	}
	if cfg.DatabaseURL == "" {
		return nil, errors.New("DATABASE_URL is not set")
	}
	fetch, err := openFetch(cfg)
	if err != nil {
		return nil, fmt.Errorf("outbound client: %w", err)
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	if _, err := db.Migrate(ctx, pool, migrations.FS); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrations: %w", err)
	}
	keys, err := index.LoadKeys(ctx, pool, cfg.Keys, cfg.Env, log)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("signing keys: %w", err)
	}
	pub := &index.Releaser{Pool: pool, Keys: keys, PublicURL: cfg.PublicURL, TTL: cfg.Index.TTL,
		RefreshBefore: cfg.Index.RefreshBefore, Log: log}
	sink := &media.Sink{DB: pool, MaxBytes: cfg.Fetch.MaxMedia}
	p := newPorts(cfg, fetch, sink, log)
	svc := &pipeline.Service{Pool: pool, Repos: p.resolver, Keys: keys, Index: pub, Log: log,
		Profiles: newProfiles(cfg, fetch, sink, log)}
	gh := ghapi.New(ghapi.Options{HTTP: fetch, APIURL: cfg.GitHub.APIURL, RawURL: cfg.GitHub.RawURL, Token: cfg.GitHub.Token})
	nparts, err := openNotify(cfg)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("notifications: %w", err)
	}
	ntf := newNotifier(cfg, pool, dataKeys(cfg, log), nparts, log)
	return &ops{cfg: cfg, pool: pool, svc: svc, gh: gh, notify: ntf, log: log}, nil
}

func (o *ops) close() { o.pool.Close() }

func (o *ops) publisher(ctx context.Context, id, name, login, homepage string, out io.Writer) error {
	actor, err := o.svc.OperatorActor(ctx)
	if err != nil {
		return err
	}
	owner, err := o.gh.Owner(ctx, "", login)
	if err != nil {
		return fmt.Errorf("GitHub owner %s: %w", login, err)
	}
	pub, created, err := o.svc.EnsureOfficialPublisher(ctx, pipeline.OfficialPublisher{Actor: actor, ID: id, Name: name, Homepage: homepage,
		Owner: pipeline.Owner{ID: owner.ID, Login: owner.Login, Type: owner.Type}})
	if err != nil {
		return refusalErr(err)
	}
	fmt.Fprintf(out, "publisher %s (%s): GitHub %s id %d (%s), status %s, verified %v, official %v, created %v, avatar %q, description %q\n",
		pub.ID, pub.Name, pub.GitHubOwner, pub.GitHubOwnerID, pub.OwnerType, pub.Status, pub.Verified, pub.Official, created,
		pub.AvatarSHA256, pub.Description)
	return nil
}

func (o *ops) importOne(ctx context.Context, in pipeline.ImportRequest, approve bool, timeout time.Duration, out io.Writer) error {
	actor, err := o.svc.OperatorActor(ctx)
	if err != nil {
		return err
	}
	in.Actor = actor
	sub, err := o.svc.Import(ctx, in)
	if err != nil {
		return refusalErr(err)
	}
	fmt.Fprintf(out, "submission %d: %s at %s queued (%s)\n", sub.ID, sub.Repo, sub.Ref, pipeline.OperatorConsent)
	sub, err = waitDecidable(ctx, o.pool, sub.ID, timeout)
	if err != nil {
		return err
	}
	checks, err := store.ChecksOf(ctx, o.pool, sub.ID, sub.Attempt)
	if err != nil {
		return err
	}
	printChecks(out, checks)
	switch domain.SubmissionStatus(sub.Status) {
	case domain.SubFailed:
		return fmt.Errorf("submission %d failed: %s: %s", sub.ID, sub.FailureCode, sub.FailureMessage)
	case domain.SubNeedsReview:
	default:
		return fmt.Errorf("submission %d is %s", sub.ID, sub.Status)
	}
	fmt.Fprintf(out, "submission %d: %s %s waits for review\n", sub.ID, sub.Name, sub.Version)
	if !approve {
		return nil
	}
	res, err := o.svc.Approve(ctx, sub.ID, pipeline.Decision{Actor: actor, Note: pipeline.OperatorConsent})
	if err != nil {
		return refusalErr(err)
	}
	fmt.Fprintf(out, "approved: %s %s (commit %s), index serial %d\n", res.App.Name, res.Version.Version, res.Version.Commit, res.Serial)
	return nil
}

func (o *ops) conversions(ctx context.Context, app, version string, l *domain.OperatorImport, out io.Writer) error {
	actor, err := o.svc.OperatorActor(ctx)
	if err != nil {
		return err
	}
	res, err := o.svc.SetConversions(ctx, pipeline.ConversionsRequest{Actor: actor, App: app, Version: version, Listing: l})
	if err != nil {
		return refusalErr(err)
	}
	switch {
	case res.Before == res.After:
		fmt.Fprintf(out, "conversions of %s %s unchanged: %d entries, %d pairs (sha256 %s)\n", res.App, res.Version, res.Entries, res.Pairs, orNone(res.After))
	case res.Entries == 0:
		fmt.Fprintf(out, "conversions of %s %s removed (was sha256 %s, from the %s)\n", res.App, res.Version, res.Before, res.Source)
	default:
		fmt.Fprintf(out, "conversions of %s %s set: %d entries, %d pairs, sha256 %s (was %s); no index published, the index does not carry them\n",
			res.App, res.Version, res.Entries, res.Pairs, res.After, orNone(res.Before))
	}
	return nil
}

// publishIndex re-signs the index as the operator account, through the same
// service call as the administration page's "Publish now".
func (o *ops) publishIndex(ctx context.Context, out io.Writer) error {
	actor, err := o.svc.OperatorActor(ctx)
	if err != nil {
		return err
	}
	rel, err := o.svc.PublishNow(ctx, actor)
	if err != nil {
		return refusalErr(err)
	}
	fmt.Fprintf(out, "index serial %d published: sha256 %s, key %s, expires %s\n",
		rel.Serial, rel.SHA256, rel.KeyID, rel.ExpiresAt.UTC().Format(time.RFC3339))
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// waitDecidable polls until the submission leaves the pipeline's automated
// steps (the server's workers run them).
func waitDecidable(ctx context.Context, pool *pgxpool.Pool, id int64, timeout time.Duration) (store.Submission, error) {
	deadline := time.Now().Add(timeout)
	for {
		sub, err := store.GetSubmission(ctx, pool, id)
		if err != nil {
			return sub, err
		}
		switch domain.SubmissionStatus(sub.Status) {
		case domain.SubQueued, domain.SubResolving, domain.SubValidating:
		default:
			return sub, nil
		}
		if time.Now().After(deadline) {
			return sub, fmt.Errorf("submission %d is still %s after %s (the server's workers run it; it stays queued)", id, sub.Status, timeout)
		}
		select {
		case <-ctx.Done():
			return sub, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func printChecks(out io.Writer, checks []domain.CheckResult) {
	for _, c := range checks {
		name := string(c.Kind)
		if c.Validator != "" {
			name += "[" + c.Validator + "]"
		}
		line := fmt.Sprintf("  %-22s %s", name, c.Status)
		if c.Code != "" {
			line += " " + c.Code
		}
		if c.Message != "" {
			line += ": " + c.Message
		}
		fmt.Fprintln(out, line)
	}
}

// refusalErr writes a pipeline refusal as "code: message".
func refusalErr(err error) error {
	var r *pipeline.Refusal
	if errors.As(err, &r) {
		return errors.New(r.Code + ": " + r.Message)
	}
	return err
}

// loadListing reads an operator's listing: a file with the .filex-apps.json
// schema (held to it by the same parser), the Markdown file its description
// names and the images it names, every path relative to the listing's
// directory (the schema's path rule keeps them inside it: no absolute path,
// no ".."). Sizes are capped as for a repository's files; the images are
// checked by the resolver and the media sink like any other.
func loadListing(path string, maxMedia int64) (*domain.OperatorImport, error) {
	b, err := readCapped(path, consent.MaxBytes)
	if err != nil {
		return nil, fmt.Errorf("listing: %w", err)
	}
	c, err := consent.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	op := &domain.OperatorImport{Listing: *c, Files: map[string][]byte{}}
	sum := sha256.Sum256(b)
	op.Source = filepath.ToSlash(path) + " sha256:" + hex.EncodeToString(sum[:])
	if c.Description != "" {
		d, err := readCapped(filepath.Join(dir, filepath.FromSlash(c.Description)), resolve.MaxDescriptionBytes)
		if err != nil {
			return nil, fmt.Errorf("description: %w", err)
		}
		if !utf8.Valid(d) || len(strings.TrimSpace(string(d))) == 0 {
			return nil, fmt.Errorf("description %s: not UTF-8 text, or empty", c.Description)
		}
		op.Description = string(d)
	}
	var images []string
	if c.Icon != "" {
		images = append(images, c.Icon)
	}
	for _, s := range c.Screenshots {
		images = append(images, s.Path)
	}
	if maxMedia <= 0 {
		maxMedia = 5 << 20
	}
	for _, p := range images {
		if _, ok := op.Files[p]; ok {
			continue
		}
		img, err := readCapped(filepath.Join(dir, filepath.FromSlash(p)), maxMedia)
		if err != nil {
			return nil, fmt.Errorf("image: %w", err)
		}
		op.Files[p] = img
	}
	return op, nil
}

// loadConversions reads an operator's listing for the conversions command:
// the file alone, held to the .filex-apps.json schema by the same parser as
// an import's (the files it names are not read; only its conversions are
// used).
func loadConversions(path string) (*domain.OperatorImport, error) {
	b, err := readCapped(path, consent.MaxBytes)
	if err != nil {
		return nil, fmt.Errorf("listing: %w", err)
	}
	c, err := consent.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", path, err)
	}
	sum := sha256.Sum256(b)
	return &domain.OperatorImport{Listing: *c, Source: filepath.ToSlash(path) + " sha256:" + hex.EncodeToString(sum[:])}, nil
}

// readCapped reads a regular file of at most max bytes.
func readCapped(path string, max int64) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if st.Size() > max {
		return nil, fmt.Errorf("%s is %d bytes, at most %d", path, st.Size(), max)
	}
	return os.ReadFile(path)
}

// readSecretLine is the first line of r, trimmed (a secret handed to an
// operator command).
func readSecretLine(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(b), "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("nothing was given")
	}
	return line, nil
}

// eventList reads -events: "all" is every event of the store.
func eventList(s string) []string {
	if strings.TrimSpace(s) == "all" {
		var out []string
		for _, k := range notify.Kinds {
			out = append(out, k.Kind)
		}
		return out
	}
	var out []string
	for _, e := range strings.Split(s, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// notifyAccount is the account of a GitHub login, for the notify commands.
func (o *ops) notifyAccount(ctx context.Context, login string) (int64, error) {
	id, err := store.AccountByGitHubLogin(ctx, o.pool, login)
	if errors.Is(err, store.ErrNotFound) {
		return 0, fmt.Errorf("no account signs in as the GitHub login %s (it signs in once first)", login)
	}
	return id, err
}

func (o *ops) notifyWebhook(ctx context.Context, login string, in notify.WebhookCreate, test bool, out io.Writer) error {
	account, err := o.notifyAccount(ctx, login)
	if err != nil {
		return err
	}
	op, err := o.svc.OperatorActor(ctx)
	if err != nil {
		return err
	}
	// Admin: the operator may choose the administrators' events for the
	// account; the fan-out still asks GitHub whether it is one.
	created, err := o.notify.CreateWebhook(ctx, notify.Actor{AccountID: account, Admin: true, Operator: op.AccountID}, in)
	if err != nil {
		return err
	}
	w := created.Webhook
	fmt.Fprintf(out, "webhook %s (%s) for %s: %s %s, events %s, header %q, signed %v\n",
		w.ID, w.Name, login, w.Format, w.URLHint, strings.Join(w.Events, ","), w.HeaderName, w.Signed)
	if test {
		return o.printTest(ctx, account, w.ID, w.Name, out)
	}
	return nil
}

func (o *ops) notifyTest(ctx context.Context, login string, out io.Writer) error {
	account, err := o.notifyAccount(ctx, login)
	if err != nil {
		return err
	}
	hooks, err := store.WebhooksOf(ctx, o.pool, account)
	if err != nil {
		return err
	}
	if len(hooks) == 0 {
		return fmt.Errorf("%s has no webhook", login)
	}
	for _, h := range hooks {
		if err := o.printTest(ctx, account, h.ID, h.Name, out); err != nil {
			return err
		}
	}
	return nil
}

func (o *ops) printTest(ctx context.Context, account int64, id, name string, out io.Writer) error {
	res, err := o.notify.TestWebhook(ctx, account, id)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "test %s (%s): %s, HTTP %d %s\n", id, name, res.Status, res.HTTPStatus, res.Error)
	if res.Status != notify.StatusDelivered {
		return errors.New("the test was not delivered")
	}
	return nil
}
