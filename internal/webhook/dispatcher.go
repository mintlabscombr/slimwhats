// Package webhook implements the outbound webhook delivery pipeline:
// normalized-event queue, worker pool, exponential-backoff retry,
// per-instance semaphore, and a DB-backed delivery log.
package webhook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/mauroneto/slimwhats/internal/instance"
)

// Event is the normalized event envelope sent to the receiver.
type Event struct {
	Event      string                 `json:"event"`
	InstanceID string                 `json:"instance_id"`
	Timestamp  string                 `json:"timestamp"`
	Data       map[string]interface{} `json:"data"`
}

// Config holds the dispatcher knobs.
type Config struct {
	WorkerCount int           // default 8
	QueueSize   int           // default 1024
	MaxAttempts int           // default 8
	BaseDelay   time.Duration // default 10s
	PerInstance int           // per-instance semaphore, default 4
	HTTPTimeout time.Duration // per-attempt, default 10s
}

// DefaultConfig returns Config with the PRD defaults.
func DefaultConfig() Config {
	return Config{
		WorkerCount: 8,
		QueueSize:   1024,
		MaxAttempts: 8,
		BaseDelay:   10 * time.Second,
		PerInstance: 4,
		HTTPTimeout: 10 * time.Second,
	}
}

// Dispatcher is the webhook delivery worker pool.
type Dispatcher struct {
	cfg    Config
	db     *sql.DB
	store  *instance.Store
	client *http.Client
	ch     chan job
	wg     sync.WaitGroup
	sem    map[string]chan struct{} // per-instance semaphore
	mu     sync.Mutex
}

type job struct {
	deliveryID string
	instanceID string
	url        string
	secret     string
	event      Event
}

// NewDispatcher creates a dispatcher. Call Start to launch the worker
// pool, Shutdown to drain and stop.
func NewDispatcher(db *sql.DB, store *instance.Store, cfg Config) *Dispatcher {
	if cfg.WorkerCount <= 0 {
		cfg.WorkerCount = 8
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 1024
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 8
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 10 * time.Second
	}
	if cfg.PerInstance <= 0 {
		cfg.PerInstance = 4
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 10 * time.Second
	}
	return &Dispatcher{
		cfg:    cfg,
		db:     db,
		store:  store,
		client: &http.Client{Timeout: cfg.HTTPTimeout},
		ch:     make(chan job, cfg.QueueSize),
		sem:    make(map[string]chan struct{}),
	}
}

// Start launches the worker pool.
func (d *Dispatcher) Start() {
	for i := 0; i < d.cfg.WorkerCount; i++ {
		d.wg.Add(1)
		go d.worker(i)
	}
}

// Enqueue queues a job for delivery. Non-blocking: if the queue is
// full, the job is dropped and logged (PRD: in-process overflow strategy).
func (d *Dispatcher) Enqueue(instanceID, eventName, deliveryID string, ev Event) bool {
	url, secret, err := d.store.LoadWebhookSecret(instanceID)
	if err != nil {
		slog.Warn("load webhook secret failed", "id", instanceID, "err", err)
		return false
	}
	if url == "" {
		// No webhook configured for this instance
		return false
	}
	select {
	case d.ch <- job{
		deliveryID: deliveryID,
		instanceID: instanceID,
		url:        url,
		secret:     secret,
		event:      ev,
	}:
		return true
	default:
		slog.Warn("webhook queue full, dropping event",
			"id", instanceID, "event", eventName, "delivery_id", deliveryID)
		return false
	}
}

// Shutdown closes the channel and waits for the workers to drain (up
// to the context's deadline).
func (d *Dispatcher) Shutdown(ctx context.Context) {
	close(d.ch)
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		slog.Warn("webhook dispatcher shutdown timeout")
	}
}

func (d *Dispatcher) worker(id int) {
	defer d.wg.Done()
	for j := range d.ch {
		d.deliver(id, j)
	}
}

func (d *Dispatcher) deliver(workerID int, j job) {
	// Per-instance semaphore
	sem := d.semFor(j.instanceID)
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-time.After(5 * time.Second):
		slog.Warn("per-instance semaphore timeout, dropping", "id", j.instanceID)
		return
	}

	// Retry loop
	for attempt := 1; attempt <= d.cfg.MaxAttempts; attempt++ {
		ok, statusCode, errStr := d.attempt(j)
		d.updateDelivery(j.deliveryID, attempt, ok, statusCode, errStr)
		if ok {
			return
		}
		if isPermanentFailure(statusCode) {
			slog.Info("webhook permanent failure, no retry",
				"id", j.instanceID, "delivery_id", j.deliveryID,
				"status", statusCode, "attempt", attempt)
			return
		}
		if attempt == d.cfg.MaxAttempts {
			slog.Warn("webhook dead after max attempts",
				"id", j.instanceID, "delivery_id", j.deliveryID, "attempts", attempt)
			return
		}
		// Backoff with jitter
		delay := backoff(d.cfg.BaseDelay, attempt, 0.2)
		slog.Debug("webhook retry scheduled",
			"id", j.instanceID, "delivery_id", j.deliveryID,
			"attempt", attempt, "delay", delay, "status", statusCode)
		time.Sleep(delay)
	}
}

func (d *Dispatcher) attempt(j job) (bool, int, string) {
	body, err := json.Marshal(j.event)
	if err != nil {
		return false, 0, fmt.Sprintf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, j.url, bytes.NewReader(body))
	if err != nil {
		return false, 0, fmt.Sprintf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Secret", j.secret)
	req.Header.Set("X-Webhook-Event", j.event.Event)
	req.Header.Set("User-Agent", "slimwhats/1.0")
	req.Header.Set("X-Webhook-Delivery-Id", j.deliveryID)
	req.Header.Set("X-Webhook-Attempt", strconv.Itoa(currentAttempt()))

	resp, err := d.client.Do(req)
	if err != nil {
		return false, 0, err.Error()
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, resp.StatusCode, ""
	}
	return false, resp.StatusCode, fmt.Sprintf("status %d", resp.StatusCode)
}

func (d *Dispatcher) semFor(instanceID string) chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s, ok := d.sem[instanceID]; ok {
		return s
	}
	s := make(chan struct{}, d.cfg.PerInstance)
	d.sem[instanceID] = s
	return s
}

func (d *Dispatcher) updateDelivery(deliveryID string, attempt int, ok bool, statusCode int, errStr string) {
	status := "succeeded"
	if !ok {
		status = "failed"
		if attempt == d.cfg.MaxAttempts {
			status = "dead"
		}
	}
	_, _ = d.db.Exec(
		`UPDATE webhook_deliveries SET attempts = ?, last_status_code = ?, last_error = ?, status = ?, updated_at = ? WHERE id = ?`,
		attempt, nullableInt(statusCode), nullableStr(errStr), status, time.Now().UTC(), deliveryID,
	)
}

func nullableInt(n int) interface{} {
	if n == 0 {
		return nil
	}
	return n
}

func nullableStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// isPermanentFailure returns true for 4xx (except 408/429) — those
// should not be retried.
func isPermanentFailure(code int) bool {
	if code >= 400 && code < 500 {
		return code != 408 && code != 429
	}
	return false
}

// backoff returns the delay for the n-th retry, with ±jitterPct jitter
// applied to the base * 2^(n-1) result. jitterPct is 0.0..1.0.
func backoff(base time.Duration, n int, jitterPct float64) time.Duration {
	d := float64(base) * math.Pow(2, float64(n-1))
	if jitterPct > 0 {
		j := (rand.Float64()*2 - 1) * jitterPct * d
		d = d + j
	}
	if d < 0 {
		d = 0
	}
	return time.Duration(d)
}

// currentAttempt is a process-wide attempt counter (for the
// X-Webhook-Attempt header). 1-based.
var currentAttemptN atomicInt

type atomicInt struct{ v int }

func (a *atomicInt) next() int {
	a.v++
	return a.v
}

func currentAttempt() int { return currentAttemptN.next() }

// RecordDelivery creates a webhook_deliveries row for an event before
// enqueueing. Returns the delivery ID.
func (d *Dispatcher) RecordDelivery(instanceID, eventType string, payload []byte) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	_, err = d.db.Exec(`
		INSERT INTO webhook_deliveries
			(id, instance_id, event_type, payload, status, attempts, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'pending', 0, ?, ?)`,
		id, instanceID, eventType, string(payload), now, now,
	)
	if err != nil {
		return "", err
	}
	return id, nil
}

// IDGen is used to produce delivery IDs. Standalone function so the
// caller can pass it into RecordDelivery.
var IDGen = func() (string, error) { return newID() }

// HashedID returns a stable ID derived from the event payload + instance
// + timestamp. Used for deduplication if needed in the future.
func HashedID(instanceID, eventName string, ts time.Time) string {
	h := sha256.Sum256([]byte(instanceID + "|" + eventName + "|" + ts.Format(time.RFC3339Nano)))
	return fmt.Sprintf("%x", h[:16])
}
