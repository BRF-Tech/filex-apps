package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/notify"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// SubmissionJob is the payload of resolve and dry_run jobs.
type SubmissionJob struct {
	SubmissionID int64 `json:"submission_id"`
	Attempt      int   `json:"attempt"`
}

// PublishJob is the payload of a publish_index job.
type PublishJob struct {
	Reason string `json:"reason"`
	Actor  *int64 `json:"actor,omitempty"`
}

// Runner works the job queue: resolve, dry runs, index publishing, and the
// periodic upkeep (expired leases, expired sessions, the index refresh).
type Runner struct {
	Pool      *pgxpool.Pool
	Resolver  Resolver
	DryRunner DryRunner
	// StorageDryRunner runs a storage plugin's conformance on the plugin
	// validators (internal/plugincheck). Nil: no plugin validator is
	// configured, and a storage plugin's submission fails its dry run.
	StorageDryRunner DryRunner
	Index            *index.Releaser
	Log              *slog.Logger
	// Permissions is filex's permission rule for re-checking the dry runs
	// (resolve.NormalizePermissions, resolve.IsDerivedPermission).
	Permissions PermissionRule

	ResolveTimeout time.Duration // per resolve (default 5 min)
	DryRunTimeout  time.Duration // per dry-run job (default 4 min per validator)
	Validators     int           // how many validators (for the dry-run time limit)
	// StorageValidators is how many plugin validators there are (the
	// dry-run time limit counts the larger of the two sets).
	StorageValidators int
	Poll              time.Duration // queue poll when idle (default 1 s)

	worker string
	once   sync.Once
	mu     sync.Mutex
	kicks  []chan struct{}
}

func (r *Runner) init() {
	r.once.Do(func() {
		host, _ := os.Hostname()
		r.worker = host + ":" + strconv.Itoa(os.Getpid())
		if r.ResolveTimeout == 0 {
			r.ResolveTimeout = 5 * time.Minute
		}
		if r.DryRunTimeout == 0 {
			n := r.Validators
			if r.StorageValidators > n {
				n = r.StorageValidators
			}
			if n < 1 {
				n = 1
			}
			r.DryRunTimeout = time.Duration(n)*4*time.Minute + time.Minute
		}
		if r.Poll == 0 {
			r.Poll = time.Second
		}
		if r.Log == nil {
			r.Log = slog.Default()
		}
	})
}

// Kick wakes every idle worker (a job was just queued).
func (r *Runner) Kick() {
	r.init()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.kicks {
		select {
		case k <- struct{}{}:
		default:
		}
	}
}

func (r *Runner) newKick() chan struct{} {
	k := make(chan struct{}, 1)
	r.mu.Lock()
	r.kicks = append(r.kicks, k)
	r.mu.Unlock()
	return k
}

// Run starts the workers and blocks until ctx ends. Dry runs are one at a
// time (a large module compiles for tens of seconds in filex, and the dry
// runner allows one call in flight per validator anyway).
func (r *Runner) Run(ctx context.Context) {
	r.init()
	var wg sync.WaitGroup
	loop := func(kinds ...string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.work(ctx, kinds)
		}()
	}
	loop(store.JobResolve)
	loop(store.JobResolve)
	loop(store.JobDryRun)
	loop(store.JobPublishIndex, store.JobRefreshIndex, store.JobExpireSessions)
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.upkeep(ctx)
	}()
	wg.Wait()
}

func (r *Runner) work(ctx context.Context, kinds []string) {
	kick := r.newKick()
	for ctx.Err() == nil {
		did, err := r.Step(ctx, kinds...)
		if err != nil && ctx.Err() == nil {
			r.Log.Error("job queue", "kinds", kinds, "err", err)
		}
		if did && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
		case <-kick:
		case <-time.After(r.Poll):
		}
	}
}

func (r *Runner) upkeep(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	last := time.Time{}
	for {
		if n, err := store.RequeueExpired(ctx, r.Pool); err != nil && ctx.Err() == nil {
			r.Log.Error("requeue expired jobs", "err", err)
		} else if n > 0 {
			r.Log.Warn("requeued jobs whose worker went away", "count", n)
		}
		if time.Since(last) > time.Hour {
			last = time.Now()
			if _, err := store.DeleteExpiredSessions(ctx, r.Pool); err != nil && ctx.Err() == nil {
				r.Log.Error("expire sessions", "err", err)
			}
			if r.Index != nil && r.Index.Keys != nil && r.Index.Keys.Index != nil {
				if _, err := r.Index.EnsureFresh(ctx); err != nil && ctx.Err() == nil {
					r.Log.Error("index refresh", "err", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Step claims one job of the kinds and works it. It reports whether there
// was one.
func (r *Runner) Step(ctx context.Context, kinds ...string) (bool, error) {
	r.init()
	lease := r.ResolveTimeout + time.Minute
	if r.DryRunTimeout+time.Minute > lease {
		lease = r.DryRunTimeout + time.Minute
	}
	job, err := store.Claim(ctx, r.Pool, kinds, r.worker, lease)
	if err != nil || job == nil {
		return false, err
	}
	switch job.Kind {
	case store.JobResolve:
		err = r.resolve(ctx, job)
	case store.JobDryRun:
		err = r.dryRun(ctx, job)
	case store.JobPublishIndex:
		err = r.publish(ctx, job)
	case store.JobRefreshIndex:
		if r.Index != nil {
			_, err = r.Index.EnsureFresh(ctx)
		}
		err = r.settle(ctx, job, err)
	case store.JobExpireSessions:
		_, err = store.DeleteExpiredSessions(ctx, r.Pool)
		err = r.settle(ctx, job, err)
	default:
		err = store.FailJob(ctx, r.Pool, job.ID, "unknown job kind "+job.Kind)
	}
	return true, err
}

// backoff is the delay before attempt n+1: 10 s, 30 s, 90 s, ... (max 15 min).
func backoff(attempts int) time.Duration {
	d := 10 * time.Second
	for i := 1; i < attempts; i++ {
		d *= 3
		if d > 15*time.Minute {
			return 15 * time.Minute
		}
	}
	return d
}

// settle completes a job, or retries it (with backoff) or fails it.
func (r *Runner) settle(ctx context.Context, job *store.Job, err error) error {
	if err == nil {
		return store.CompleteJob(ctx, r.Pool, job.ID)
	}
	if job.Attempts < job.MaxAttempts {
		r.Log.Warn("job failed, will retry", "job", job.ID, "kind", job.Kind, "attempt", job.Attempts, "err", err)
		return store.RetryJob(ctx, r.Pool, job.ID, err.Error(), backoff(job.Attempts))
	}
	r.Log.Error("job failed for good", "job", job.ID, "kind", job.Kind, "err", err)
	return store.FailJob(ctx, r.Pool, job.ID, err.Error())
}

func (r *Runner) publish(ctx context.Context, job *store.Job) error {
	var p PublishJob
	if err := json.Unmarshal(job.Payload, &p); err != nil || p.Reason == "" {
		return store.FailJob(ctx, r.Pool, job.ID, "bad payload")
	}
	if r.Index == nil {
		return store.FailJob(ctx, r.Pool, job.ID, "no index publisher")
	}
	_, err := r.Index.Publish(ctx, p.Reason, p.Actor, false)
	if errors.Is(err, index.ErrNoKey) {
		return store.FailJob(ctx, r.Pool, job.ID, err.Error())
	}
	return r.settle(ctx, job, err)
}

// resolve runs checks 2-7 (the resolver) and 5 (the policy, here), then
// queues the dry runs or fails the attempt.
func (r *Runner) resolve(ctx context.Context, job *store.Job) error {
	var p SubmissionJob
	if err := json.Unmarshal(job.Payload, &p); err != nil || p.SubmissionID == 0 {
		return store.FailJob(ctx, r.Pool, job.ID, "bad payload")
	}
	sub, err := store.GetSubmission(ctx, r.Pool, p.SubmissionID)
	if errors.Is(err, store.ErrNotFound) {
		return store.CompleteJob(ctx, r.Pool, job.ID)
	}
	if err != nil {
		return r.settle(ctx, job, err)
	}
	// A withdrawn submission, or one retried since this job was queued.
	if sub.Attempt != p.Attempt || (sub.Status != string(domain.SubQueued) && sub.Status != string(domain.SubResolving)) {
		return store.CompleteJob(ctx, r.Pool, job.ID)
	}
	if sub.Status == string(domain.SubQueued) {
		if ok, err := store.Transition(ctx, r.Pool, sub.ID, sub.Attempt, strs(domain.SubQueued), string(domain.SubResolving)); err != nil || !ok {
			if err != nil {
				return r.settle(ctx, job, err)
			}
			return store.CompleteJob(ctx, r.Pool, job.ID)
		}
	}
	if r.Resolver == nil {
		r.failInfra(ctx, sub, strs(domain.SubResolving), "resolver_unavailable", "The resolver is not configured on this server.")
		return store.FailJob(ctx, r.Pool, job.ID, "no resolver")
	}
	// An operator import carries its listing (nil for a web submission).
	op, err := operatorImport(ctx, r.Pool, sub)
	if errors.Is(err, errOperatorListing) {
		_, _ = store.Fail(ctx, r.Pool, sub.ID, sub.Attempt, strs(domain.SubResolving), "operator_listing_unreadable", err.Error())
		return store.FailJob(ctx, r.Pool, job.ID, err.Error())
	}
	if err != nil {
		return r.settle(ctx, job, err)
	}
	rctx, cancel := context.WithTimeout(ctx, r.ResolveTimeout)
	res, checks, rerr := r.Resolver.Resolve(rctx, domain.ResolveRequest{
		SubmissionID: sub.ID, Repo: sub.Repo, Ref: sub.Ref, Publisher: sub.PublisherID, Operator: op,
		Kind: domain.SubmissionKind(sub.Kind),
	})
	cancel()
	if rerr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if job.Attempts < job.MaxAttempts {
			_, _ = store.Transition(ctx, r.Pool, sub.ID, sub.Attempt, strs(domain.SubResolving), string(domain.SubQueued))
			r.Log.Warn("resolve failed, will retry", "submission", sub.ID, "attempt", job.Attempts, "err", rerr)
			return store.RetryJob(ctx, r.Pool, job.ID, rerr.Error(), backoff(job.Attempts))
		}
		r.failInfra(ctx, sub, strs(domain.SubResolving, domain.SubQueued), "resolve_error",
			"The repository could not be read: "+rerr.Error())
		return store.FailJob(ctx, r.Pool, job.ID, rerr.Error())
	}

	now := time.Now()
	err = db.InTx(ctx, r.Pool, func(tx pgx.Tx) error {
		all := append([]domain.CheckResult{}, checks...)
		if _, failed := FirstFailure(all); !failed && res != nil {
			facts, err := LoadPolicyFacts(ctx, tx, res)
			if err != nil {
				return err
			}
			all = append(all, Policy(res, sub.PublisherID, facts, now))
		}
		if err := store.InsertChecks(ctx, tx, sub.ID, sub.Attempt, all); err != nil {
			return err
		}
		if f, failed := FirstFailure(all); failed {
			_, err := store.Fail(ctx, tx, sub.ID, sub.Attempt, strs(domain.SubResolving), orCode(f.Code, string(f.Kind)+"_failed"), f.Message)
			if err != nil {
				return err
			}
			return store.CompleteJob(ctx, tx, job.ID)
		}
		if res == nil {
			if _, err := store.Fail(ctx, tx, sub.ID, sub.Attempt, strs(domain.SubResolving), "resolve_incomplete",
				"The resolver returned no result and no failed check."); err != nil {
				return err
			}
			return store.CompleteJob(ctx, tx, job.ID)
		}
		ok, err := store.SetResolved(ctx, tx, sub.ID, sub.Attempt, res)
		if err != nil {
			return err
		}
		if ok {
			if _, err := store.Enqueue(ctx, tx, store.JobDryRun, SubmissionJob{SubmissionID: sub.ID, Attempt: sub.Attempt}, time.Time{}); err != nil {
				return err
			}
		}
		return store.CompleteJob(ctx, tx, job.ID)
	})
	if err != nil {
		return r.settle(ctx, job, err)
	}
	r.Kick()
	return nil
}

// dryRun asks the validators (check 8) and moves the submission to review
// or fails the attempt.
func (r *Runner) dryRun(ctx context.Context, job *store.Job) error {
	var p SubmissionJob
	if err := json.Unmarshal(job.Payload, &p); err != nil || p.SubmissionID == 0 {
		return store.FailJob(ctx, r.Pool, job.ID, "bad payload")
	}
	sub, err := store.GetSubmission(ctx, r.Pool, p.SubmissionID)
	if errors.Is(err, store.ErrNotFound) {
		return store.CompleteJob(ctx, r.Pool, job.ID)
	}
	if err != nil {
		return r.settle(ctx, job, err)
	}
	if sub.Attempt != p.Attempt || sub.Status != string(domain.SubValidating) {
		return store.CompleteJob(ctx, r.Pool, job.ID)
	}
	var res domain.Resolution
	if err := json.Unmarshal(sub.Resolution, &res); err != nil {
		_, _ = store.Fail(ctx, r.Pool, sub.ID, sub.Attempt, strs(domain.SubValidating), "resolution_unreadable", "The stored resolution could not be read.")
		return store.FailJob(ctx, r.Pool, job.ID, err.Error())
	}
	dryRunner := r.DryRunner
	if res.Kind == domain.KindStorage {
		// A storage plugin is run by the plugin validators, never asked of an
		// app validator (internal/plugincheck).
		dryRunner = r.StorageDryRunner
		if dryRunner == nil {
			r.failInfra(ctx, sub, strs(domain.SubValidating), "validator_unavailable",
				"No plugin validator is configured on this server (FAPPS_PLUGIN_VALIDATORS): a storage plugin cannot be checked.")
			return store.FailJob(ctx, r.Pool, job.ID, "no plugin validator")
		}
	}
	if dryRunner == nil {
		r.failInfra(ctx, sub, strs(domain.SubValidating), "validator_unavailable", "No validator is configured on this server.")
		return store.FailJob(ctx, r.Pool, job.ID, "no dry runner")
	}
	dctx, cancel := context.WithTimeout(ctx, r.DryRunTimeout)
	results, checks, derr := dryRunner.DryRun(dctx, DryRunRequestFor(&res))
	cancel()
	if derr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if job.Attempts < job.MaxAttempts {
			r.Log.Warn("dry run failed, will retry", "submission", sub.ID, "attempt", job.Attempts, "err", derr)
			return store.RetryJob(ctx, r.Pool, job.ID, derr.Error(), backoff(job.Attempts))
		}
		r.failInfra(ctx, sub, strs(domain.SubValidating), "dry_run_error",
			"The validators could not be asked: "+derr.Error())
		return store.FailJob(ctx, r.Pool, job.ID, derr.Error())
	}
	checks = EnforcePins(&res, results, checks, time.Now(), r.Permissions)
	err = db.InTx(ctx, r.Pool, func(tx pgx.Tx) error {
		if err := store.DeleteChecks(ctx, tx, sub.ID, sub.Attempt, domain.CheckDryRun); err != nil {
			return err
		}
		if err := store.InsertChecks(ctx, tx, sub.ID, sub.Attempt, checks); err != nil {
			return err
		}
		if ok, code, msg := DryRunOutcome(checks); !ok {
			if _, err := store.Fail(ctx, tx, sub.ID, sub.Attempt, strs(domain.SubValidating), code, msg); err != nil {
				return err
			}
		} else if ok, err := store.Transition(ctx, tx, sub.ID, sub.Attempt, strs(domain.SubValidating), string(domain.SubNeedsReview)); err != nil {
			return err
		} else if ok {
			if err := notifyReview(ctx, tx, sub, &res); err != nil {
				return err
			}
		}
		return store.CompleteJob(ctx, tx, job.ID)
	})
	if err != nil {
		return r.settle(ctx, job, err)
	}
	return nil
}

// failInfra fails an attempt because the store, not the submission, could
// not go on (no validator, no answer from the validators or GitHub), and
// tells the administrators (notify.KindPipelineError) in the same
// transaction.
func (r *Runner) failInfra(ctx context.Context, sub store.Submission, from []string, code, message string) {
	err := db.InTx(ctx, r.Pool, func(tx pgx.Tx) error {
		ok, err := store.Fail(ctx, tx, sub.ID, sub.Attempt, from, code, message)
		if err != nil || !ok {
			return err
		}
		_, err = notify.Emit(ctx, tx, notify.PipelineErrorEvent(sub, code, message))
		return err
	})
	if err != nil {
		r.Log.Error("fail a submission", "submission", sub.ID, "code", code, "err", err)
	}
}

// notifyReview tells the administrators that a submission waits for review
// (notify.KindAppSubmitted for a new app, KindVersionSubmitted for a new
// version). An operator import raises nothing: the operator is at the
// console that imports and approves it.
func notifyReview(ctx context.Context, tx pgx.Tx, sub store.Submission, res *domain.Resolution) error {
	op, err := store.OperatorListing(ctx, tx, sub.ID)
	if err != nil || op != nil {
		return err
	}
	if sub.Name == "" {
		sub.Name = res.Name
	}
	if sub.Version == "" {
		sub.Version = res.Version
	}
	exists, err := store.AppNameExists(ctx, tx, res.Name)
	if err != nil {
		return err
	}
	_, err = notify.Emit(ctx, tx, notify.ReviewEvent(sub, !exists))
	return err
}

func orCode(code, def string) string {
	if code != "" {
		return code
	}
	return def
}

// String for logs.
func (p SubmissionJob) String() string {
	return fmt.Sprintf("submission %d attempt %d", p.SubmissionID, p.Attempt)
}
