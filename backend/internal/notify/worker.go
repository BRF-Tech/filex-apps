package notify

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// MaxDeliverAttempts is how often one delivery is tried: the first time and
// three retries, 10 s, 30 s and 90 s apart.
const MaxDeliverAttempts = 4

// maxFanOutAttempts bounds the waits for the administrators' role check
// (GitHub away): after them the event goes to the administrators known.
const maxFanOutAttempts = 5

// Worker works the notifier's jobs: notify_event (an event to its channels)
// and notify_deliver (one message to one channel).
type Worker struct {
	Svc    *Service
	Admins AdminChecker
	Poll   time.Duration // idle poll (default 2 s)
	Log    *slog.Logger

	once   sync.Once
	worker string
}

func (w *Worker) init() {
	w.once.Do(func() {
		host, _ := os.Hostname()
		w.worker = host + ":" + strconv.Itoa(os.Getpid()) + ":notify"
		if w.Poll == 0 {
			w.Poll = 2 * time.Second
		}
		if w.Log == nil {
			w.Log = slog.Default()
		}
	})
}

// Run works the queue until ctx ends: two workers, so one slow receiver
// (10 s at most) does not hold the others back for long.
func (w *Worker) Run(ctx context.Context) {
	w.init()
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				did, err := w.Step(ctx)
				if err != nil && ctx.Err() == nil {
					w.Log.Error("notify: job queue", "err", err)
				}
				if did && err == nil {
					continue
				}
				select {
				case <-ctx.Done():
				case <-time.After(w.Poll):
				}
			}
		}()
	}
	wg.Wait()
}

// Step claims one notifier job and works it; it reports whether there was
// one.
func (w *Worker) Step(ctx context.Context) (bool, error) {
	w.init()
	job, err := store.Claim(ctx, w.Svc.Pool, []string{store.JobNotifyEvent, store.JobNotifyDeliver}, w.worker, 2*time.Minute)
	if err != nil || job == nil {
		return false, err
	}
	switch job.Kind {
	case store.JobNotifyEvent:
		err = w.fanOut(ctx, job)
	case store.JobNotifyDeliver:
		err = w.deliver(ctx, job)
	}
	return true, err
}

// backoff before attempt n+1: 10 s, 30 s, 90 s, ...
func backoff(attempts int) time.Duration {
	d := 10 * time.Second
	for i := 1; i < attempts; i++ {
		d *= 3
	}
	if d > 15*time.Minute {
		d = 15 * time.Minute
	}
	return d
}

// fanOut turns an event into one delivery per channel that chose it.
func (w *Worker) fanOut(ctx context.Context, job *store.Job) error {
	pool := w.Svc.Pool
	var p EventJob
	if err := json.Unmarshal(job.Payload, &p); err != nil || p.EventID == 0 {
		return store.FailJob(ctx, pool, job.ID, "bad payload")
	}
	ev, err := store.NotifyEventByID(ctx, pool, p.EventID)
	if errors.Is(err, store.ErrNotFound) {
		return store.CompleteJob(ctx, pool, job.ID)
	}
	if err != nil {
		return w.retry(ctx, job, err.Error(), maxFanOutAttempts)
	}
	var accounts []int64
	if ev.Audience == AudienceAccounts {
		accounts = ev.AccountIDs
		if len(accounts) == 0 {
			return store.CompleteJob(ctx, pool, job.ID)
		}
	}
	recips, err := store.NotifyRecipients(ctx, pool, ev.Kind, accounts)
	if err != nil {
		return w.retry(ctx, job, err.Error(), maxFanOutAttempts)
	}
	if ev.Audience == AudienceAdmin {
		var unknown bool
		recips, unknown = w.admins(ctx, recips)
		if unknown && job.Attempts < maxFanOutAttempts {
			return store.RetryJob(ctx, pool, job.ID, "the administrators' role check did not answer", backoff(job.Attempts))
		}
		if unknown {
			w.Log.Error("notify: the role check did not answer; the event goes to the administrators known", "event", ev.ID, "kind", ev.Kind)
		}
	}
	return db.InTx(ctx, pool, func(tx pgx.Tx) error {
		for _, r := range recips {
			id, inserted, err := store.InsertDelivery(ctx, tx, ev.ID, r)
			if err != nil {
				return err
			}
			if inserted {
				if _, err := store.Enqueue(ctx, tx, store.JobNotifyDeliver, DeliverJob{DeliveryID: id}, time.Time{}); err != nil {
					return err
				}
			}
		}
		return store.CompleteJob(ctx, tx, job.ID)
	})
}

// admins keeps the recipients who are administrators now; unknown is true
// when the role check did not answer for one of them (they are dropped).
func (w *Worker) admins(ctx context.Context, recips []store.Recipient) ([]store.Recipient, bool) {
	if w.Admins == nil {
		return nil, false
	}
	verdict := map[int64]bool{}
	unknown := false
	for _, r := range recips {
		if _, ok := verdict[r.AccountID]; ok {
			continue
		}
		admin, known := w.Admins.IsAdmin(ctx, r.AccountID)
		if !known {
			unknown = true
		}
		verdict[r.AccountID] = admin && known
	}
	out := slices.DeleteFunc(slices.Clone(recips), func(r store.Recipient) bool { return !verdict[r.AccountID] })
	return out, unknown
}

func (w *Worker) retry(ctx context.Context, job *store.Job, msg string, max int) error {
	if job.Attempts < max {
		return store.RetryJob(ctx, w.Svc.Pool, job.ID, msg, backoff(job.Attempts))
	}
	return store.FailJob(ctx, w.Svc.Pool, job.ID, msg)
}

// deliver sends one delivery and records the outcome on the delivery, on
// its channel (the list's "last delivery") and on the job.
func (w *Worker) deliver(ctx context.Context, job *store.Job) error {
	svc, pool := w.Svc, w.Svc.Pool
	var p DeliverJob
	if err := json.Unmarshal(job.Payload, &p); err != nil || p.DeliveryID == 0 {
		return store.FailJob(ctx, pool, job.ID, "bad payload")
	}
	d, err := store.DeliveryByID(ctx, pool, p.DeliveryID)
	if errors.Is(err, store.ErrNotFound) {
		// The channel was removed (its deliveries went with it).
		return store.CompleteJob(ctx, pool, job.ID)
	}
	if err != nil {
		return w.retry(ctx, job, err.Error(), MaxDeliverAttempts)
	}
	if d.Status != "queued" {
		return store.CompleteJob(ctx, pool, job.ID)
	}
	if svc.Data == nil {
		_ = store.SetDeliveryResult(ctx, pool, d.ID, StatusFailed, 0, "FAPPS_DATA_KEY is not set")
		return store.FailJob(ctx, pool, job.ID, "no data key")
	}
	ev, err := store.NotifyEventByID(ctx, pool, d.EventID)
	if err != nil {
		return w.retry(ctx, job, err.Error(), MaxDeliverAttempts)
	}
	m := Render(ev.Kind, ev.Data, svc.PublicURL, svc.name())

	var res Result
	switch {
	case d.WebhookID != "":
		row, err := store.WebhookByID(ctx, pool, d.WebhookID)
		if errors.Is(err, store.ErrNotFound) {
			return store.CompleteJob(ctx, pool, job.ID)
		}
		if err != nil {
			return w.retry(ctx, job, err.Error(), MaxDeliverAttempts)
		}
		hook, err := svc.openWebhook(row)
		if err != nil {
			_ = store.SetDeliveryResult(ctx, pool, d.ID, StatusFailed, 0, "the webhook's sealed fields do not open")
			return store.FailJob(ctx, pool, job.ID, "sealed fields do not open")
		}
		res = svc.Sender.SendWebhook(ctx, hook, m, Meta{Delivery: strconv.FormatInt(d.ID, 10), Event: ev.Kind,
			OccurredAt: ev.CreatedAt, Source: svc.source(), Store: svc.name(), Data: ev.Data})
		if err := store.SetWebhookResult(ctx, pool, row.ID, res.Status, res.HTTPStatus, res.Error); err != nil {
			w.Log.Error("notify: record a delivery on its webhook", "webhook", row.ID, "err", err)
		}
	case d.PushID != "":
		row, err := store.PushByID(ctx, pool, d.PushID)
		if errors.Is(err, store.ErrNotFound) {
			return store.CompleteJob(ctx, pool, job.ID)
		}
		if err != nil {
			return w.retry(ctx, job, err.Error(), MaxDeliverAttempts)
		}
		target, err := svc.openPush(row)
		if err != nil {
			_ = store.SetDeliveryResult(ctx, pool, d.ID, StatusFailed, 0, "the subscription's sealed fields do not open")
			return store.FailJob(ctx, pool, job.ID, "sealed fields do not open")
		}
		res = svc.Sender.SendPush(ctx, svc.VAPID, target, m, ev.Kind+":"+strconv.FormatInt(ev.ID, 10))
		if res.Gone {
			// The browser unsubscribed or the subscription expired: forget
			// it (its deliveries go with it).
			if err := store.DeletePushByID(ctx, pool, row.ID); err != nil {
				return w.retry(ctx, job, err.Error(), MaxDeliverAttempts)
			}
			return store.CompleteJob(ctx, pool, job.ID)
		}
		if err := store.SetPushResult(ctx, pool, row.ID, res.Status, res.HTTPStatus, res.Error); err != nil {
			w.Log.Error("notify: record a delivery on its subscription", "push", row.ID, "err", err)
		}
	default:
		return store.FailJob(ctx, pool, job.ID, "a delivery without a channel")
	}

	switch {
	case res.Status == StatusDelivered:
		if err := store.SetDeliveryResult(ctx, pool, d.ID, StatusDelivered, res.HTTPStatus, ""); err != nil {
			return err
		}
		return store.CompleteJob(ctx, pool, job.ID)
	case res.Retry && job.Attempts < MaxDeliverAttempts:
		if err := store.SetDeliveryResult(ctx, pool, d.ID, "queued", res.HTTPStatus, res.Error); err != nil {
			return err
		}
		return store.RetryJob(ctx, pool, job.ID, res.Error, backoff(job.Attempts))
	default:
		if err := store.SetDeliveryResult(ctx, pool, d.ID, StatusFailed, res.HTTPStatus, res.Error); err != nil {
			return err
		}
		w.Log.Warn("notify: delivery failed", "delivery", d.ID, "kind", ev.Kind, "http_status", res.HTTPStatus, "why", res.Error)
		return store.FailJob(ctx, pool, job.ID, res.Error)
	}
}
