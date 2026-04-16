package runtimecore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

type JobQueue struct {
	mu           sync.RWMutex
	jobs         map[string]Job
	dead         []Job
	dispatchers  map[string]JobDispatcher
	alerts       AlertSink
	now          func() time.Time
	logger       *Logger
	tracer       *TraceRecorder
	metrics      *MetricsRegistry
	store        jobQueueStore
	lastRecovery JobRecoverySnapshot
}

const recoveryReasonRuntimeRestart = "runtime restarted during job execution"

type JobRecoverySnapshot struct {
	RecoveredAt      time.Time         `json:"recoveredAt"`
	TotalJobs        int               `json:"totalJobs"`
	RecoveredJobs    int               `json:"recoveredJobs"`
	RecoveredRunning int               `json:"recoveredRunning"`
	RetriedJobs      int               `json:"retriedJobs"`
	DeadJobs         int               `json:"deadJobs"`
	StatusCounts     map[JobStatus]int `json:"statusCounts,omitempty"`
}

type RecoverySnapshot struct {
	RecoveredAt        time.Time            `json:"recoveredAt"`
	TotalJobs          int                  `json:"totalJobs"`
	RecoveredJobs      int                  `json:"recoveredJobs"`
	RecoveredRunning   int                  `json:"recoveredRunning"`
	RetriedJobs        int                  `json:"retriedJobs"`
	DeadJobs           int                  `json:"deadJobs"`
	StatusCounts       map[JobStatus]int    `json:"statusCounts,omitempty"`
	TotalSchedules     int                  `json:"totalSchedules"`
	RecoveredSchedules int                  `json:"recoveredSchedules"`
	InvalidSchedules   int                  `json:"invalidSchedules"`
	ScheduleKinds      map[ScheduleKind]int `json:"scheduleKinds,omitempty"`
}

func cloneRecoverySnapshot(job JobRecoverySnapshot) RecoverySnapshot {
	recovery := RecoverySnapshot{
		RecoveredAt:      job.RecoveredAt,
		TotalJobs:        job.TotalJobs,
		RecoveredJobs:    job.RecoveredJobs,
		RecoveredRunning: job.RecoveredRunning,
		RetriedJobs:      job.RetriedJobs,
		DeadJobs:         job.DeadJobs,
	}
	if len(job.StatusCounts) > 0 {
		recovery.StatusCounts = make(map[JobStatus]int, len(job.StatusCounts))
		for status, count := range job.StatusCounts {
			recovery.StatusCounts[status] = count
		}
	}
	return recovery
}

func (q *JobQueue) LastRecoverySnapshot() RecoverySnapshot {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return cloneRecoverySnapshot(q.lastRecovery)
}

type jobQueueStore interface {
	SaveJob(context.Context, Job) error
	ListJobs(context.Context) ([]Job, error)
}

type AlertSink interface {
	RecordAlert(context.Context, AlertRecord) error
}

type deadLetterTransactionalAlertSink interface {
	PersistJobDeadLetter(context.Context, Job, AlertRecord) error
}

type deadLetterRetryTransactionalAlertSink interface {
	PersistJobDeadLetterRetry(context.Context, Job, string) error
}

type alertResolver interface {
	DeleteAlert(context.Context, string) error
}

type JobDispatcher interface {
	DispatchQueuedJob(context.Context, Job) error
}

func NewJobQueue() *JobQueue {
	return &JobQueue{
		jobs:        make(map[string]Job),
		dispatchers: make(map[string]JobDispatcher),
		now:         time.Now().UTC,
		logger:      NewLogger(io.Discard),
		tracer:      NewTraceRecorder(),
		metrics:     NewMetricsRegistry(),
	}
}

func (q *JobQueue) SetObservability(logger *Logger, tracer *TraceRecorder, metrics *MetricsRegistry) {
	if logger != nil {
		q.logger = logger
	}
	if tracer != nil {
		q.tracer = tracer
	}
	if metrics != nil {
		q.metrics = metrics
	}
}

func (q *JobQueue) SetStore(store jobQueueStore) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.store = store
}

func (q *JobQueue) SetAlertSink(sink AlertSink) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.alerts = sink
}

func (q *JobQueue) RegisterDispatcher(jobType string, dispatcher JobDispatcher) error {
	if dispatcher == nil {
		return errors.New("job dispatcher is required")
	}
	if jobType == "" {
		return errors.New("job type is required")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.dispatchers[jobType] = dispatcher
	return nil
}

func (q *JobQueue) DispatchReady(ctx context.Context, at time.Time) {
	for _, job := range q.ReadyJobs(at) {
		dispatcher := q.dispatcherFor(job.Type)
		if dispatcher == nil {
			continue
		}
		if err := dispatcher.DispatchQueuedJob(ctx, job); err != nil && q.logger != nil {
			_ = q.logger.Log("error", "job queue dispatcher returned error", LogContext{TraceID: job.TraceID, EventID: job.EventID, RunID: job.RunID, CorrelationID: job.Correlation}, map[string]any{"job_id": job.ID, "job_type": job.Type, "error": err.Error()})
		}
	}
}

func (q *JobQueue) dispatcherFor(jobType string) JobDispatcher {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.dispatchers[jobType]
}

func (q *JobQueue) Enqueue(ctx context.Context, job Job) error {
	if err := job.Validate(); err != nil {
		return err
	}
	if job.Status == "" {
		job.Status = JobStatusPending
	}

	q.mu.Lock()
	if _, exists := q.jobs[job.ID]; exists {
		q.mu.Unlock()
		return fmt.Errorf("job %q already exists", job.ID)
	}
	if err := persistJob(ctx, q.store, job); err != nil {
		q.mu.Unlock()
		return err
	}
	q.jobs[job.ID] = job
	q.syncMetricsLocked()
	stored := q.jobs[job.ID]
	q.mu.Unlock()
	q.observeLifecycle("job.enqueued", stored)
	return nil
}

func (q *JobQueue) Inspect(_ context.Context, id string) (Job, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	job, exists := q.jobs[id]
	if !exists {
		return Job{}, errors.New("job not found")
	}
	return job, nil
}

func (q *JobQueue) MarkRunning(ctx context.Context, id string) (Job, error) {
	return q.transition(ctx, id, JobStatusRunning, "")
}

func (q *JobQueue) Complete(ctx context.Context, id string) (Job, error) {
	return q.transition(ctx, id, JobStatusDone, "")
}

func (q *JobQueue) Fail(ctx context.Context, id string, reason string) (Job, error) {
	q.mu.Lock()

	job, exists := q.jobs[id]
	if !exists {
		q.mu.Unlock()
		return Job{}, errors.New("job not found")
	}

	if job.RetryCount < job.MaxRetries {
		nextRun := q.now().Add(simpleBackoff(job.RetryCount + 1))
		updated, err := job.Transition(JobStatusRetrying, nextRun, reason)
		if err != nil {
			q.mu.Unlock()
			return Job{}, err
		}
		if err := persistJob(ctx, q.store, updated); err != nil {
			q.mu.Unlock()
			return Job{}, err
		}
		q.jobs[id] = updated
		q.syncMetricsLocked()
		q.mu.Unlock()
		q.observeLifecycle("job.retrying", updated)
		return updated, nil
	}

	updated, err := job.Transition(JobStatusDead, q.now(), reason)
	if err != nil {
		q.mu.Unlock()
		return Job{}, err
	}
	if err := q.persistDeadLetter(ctx, updated); err != nil {
		q.mu.Unlock()
		return Job{}, err
	}
	q.jobs[id] = updated
	q.dead = append(q.dead, updated)
	q.syncMetricsLocked()
	q.mu.Unlock()
	q.observeLifecycle("job.dead_lettered", updated)
	return updated, nil
}

func (q *JobQueue) Timeout(ctx context.Context, id string) (Job, error) {
	return q.Fail(ctx, id, "timeout")
}

func (q *JobQueue) Retry(ctx context.Context, id string) (Job, error) {
	q.mu.Lock()

	job, exists := q.jobs[id]
	if !exists {
		q.mu.Unlock()
		return Job{}, errors.New("job not found")
	}

	updated, err := job.Transition(JobStatusRunning, q.now(), "")
	if err != nil {
		q.mu.Unlock()
		return Job{}, err
	}
	if err := persistJob(ctx, q.store, updated); err != nil {
		q.mu.Unlock()
		return Job{}, err
	}
	q.jobs[id] = updated
	q.syncMetricsLocked()
	q.mu.Unlock()
	q.observeLifecycle("job.retried", updated)
	return updated, nil
}

func (q *JobQueue) RetryDeadLetter(ctx context.Context, id string) (Job, error) {
	q.mu.Lock()

	job, exists := q.jobs[id]
	if !exists {
		q.mu.Unlock()
		return Job{}, errors.New("job not found")
	}
	if job.Status != JobStatusDead || !job.DeadLetter {
		q.mu.Unlock()
		return Job{}, fmt.Errorf("job %q is not dead-lettered", id)
	}

	updated := reviveDeadLetterJob(job)
	if err := q.persistDeadLetterRetry(ctx, updated); err != nil {
		q.mu.Unlock()
		return Job{}, err
	}
	q.jobs[id] = updated
	q.removeDeadLetterJobLocked(id)
	q.syncMetricsLocked()
	q.mu.Unlock()
	q.observeLifecycle("job.dead_letter_retried", updated)
	return updated, nil
}

func (q *JobQueue) Cancel(ctx context.Context, id string) (Job, error) {
	q.mu.Lock()

	job, exists := q.jobs[id]
	if !exists {
		q.mu.Unlock()
		return Job{}, errors.New("job not found")
	}
	if job.Status != JobStatusPending && job.Status != JobStatusRetrying {
		q.mu.Unlock()
		return Job{}, fmt.Errorf("job %q cannot be cancelled from status %s", id, job.Status)
	}

	updated, err := job.Transition(JobStatusCancelled, q.now(), "")
	if err != nil {
		q.mu.Unlock()
		return Job{}, err
	}
	if err := persistJob(ctx, q.store, updated); err != nil {
		q.mu.Unlock()
		return Job{}, err
	}
	q.jobs[id] = updated
	q.syncMetricsLocked()
	q.mu.Unlock()
	q.observeLifecycle("job.cancelled", updated)
	return updated, nil
}

func (q *JobQueue) DeadLetter(_ context.Context) []Job {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return append([]Job(nil), q.dead...)
}

func (q *JobQueue) removeDeadLetterJobLocked(id string) {
	for index, job := range q.dead {
		if job.ID != id {
			continue
		}
		q.dead = append(q.dead[:index], q.dead[index+1:]...)
		return
	}
}

func (q *JobQueue) List() []Job {
	q.mu.RLock()
	defer q.mu.RUnlock()
	items := make([]Job, 0, len(q.jobs))
	for _, job := range q.jobs {
		items = append(items, job)
	}
	return items
}

func (q *JobQueue) ReadyJobs(at time.Time) []Job {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if at.IsZero() {
		at = q.now()
	}
	items := make([]Job, 0, len(q.jobs))
	for _, job := range q.jobs {
		switch job.Status {
		case JobStatusPending:
			items = append(items, job)
		case JobStatusRetrying:
			if job.NextRunAt == nil || !job.NextRunAt.After(at) {
				items = append(items, job)
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return items
}

func (q *JobQueue) Restore(ctx context.Context) error {
	q.mu.RLock()
	store := q.store
	q.mu.RUnlock()
	if store == nil {
		return nil
	}

	jobs, err := store.ListJobs(ctx)
	if err != nil {
		return err
	}

	now := q.now()
	restored := make(map[string]Job, len(jobs))
	dead := make([]Job, 0)
	recovery := JobRecoverySnapshot{
		RecoveredAt:  now,
		StatusCounts: map[JobStatus]int{},
	}
	for _, job := range jobs {
		recovered, changed, recoverErr := q.recoverJob(job, now)
		if recoverErr != nil {
			return recoverErr
		}
		if changed {
			if recovered.Status == JobStatusDead {
				if err := q.persistDeadLetter(ctx, recovered); err != nil {
					return err
				}
			} else if err := persistJob(ctx, store, recovered); err != nil {
				return err
			}
			recovery.RecoveredJobs++
			recovery.RecoveredRunning++
			if q.metrics != nil {
				q.metrics.IncrementJobRecoveries()
			}
			q.observeLifecycle("job.recovered", recovered)
		}
		restored[recovered.ID] = recovered
		recovery.TotalJobs++
		recovery.StatusCounts[recovered.Status]++
		if recovered.Status == JobStatusRetrying {
			recovery.RetriedJobs++
		}
		if recovered.Status == JobStatusDead {
			dead = append(dead, recovered)
			recovery.DeadJobs++
		}
	}

	q.mu.Lock()
	q.jobs = restored
	q.dead = dead
	q.lastRecovery = recovery
	q.syncMetricsLocked()
	q.mu.Unlock()
	if q.logger != nil {
		_ = q.logger.Log("info", "job queue restored from persistence", LogContext{}, map[string]any{
			"restored_jobs":     recovery.TotalJobs,
			"recovered_jobs":    recovery.RecoveredJobs,
			"recovered_running": recovery.RecoveredRunning,
			"retrying_jobs":     recovery.RetriedJobs,
			"dead_jobs":         recovery.DeadJobs,
		})
	}
	return nil
}

func (q *JobQueue) transition(ctx context.Context, id string, next JobStatus, reason string) (Job, error) {
	q.mu.Lock()

	job, exists := q.jobs[id]
	if !exists {
		q.mu.Unlock()
		return Job{}, errors.New("job not found")
	}

	updated, err := job.Transition(next, q.now(), reason)
	if err != nil {
		q.mu.Unlock()
		return Job{}, err
	}
	if err := persistJob(ctx, q.store, updated); err != nil {
		q.mu.Unlock()
		return Job{}, err
	}
	q.jobs[id] = updated
	q.syncMetricsLocked()
	q.mu.Unlock()
	q.observeLifecycle(jobLifecycleMessage(next), updated)
	return updated, nil
}

func (q *JobQueue) recoverJob(job Job, at time.Time) (Job, bool, error) {
	switch job.Status {
	case JobStatusRunning:
		if job.RetryCount < job.MaxRetries {
			updated, err := job.Transition(JobStatusRetrying, at, recoveryReasonRuntimeRestart)
			if err != nil {
				return Job{}, false, err
			}
			return updated, true, nil
		}
		updated, err := job.Transition(JobStatusDead, at, recoveryReasonRuntimeRestart)
		if err != nil {
			return Job{}, false, err
		}
		return updated, true, nil
	default:
		return job, false, nil
	}
}

func cloneJobRecoverySnapshot(snapshot JobRecoverySnapshot) JobRecoverySnapshot {
	cloned := snapshot
	if len(snapshot.StatusCounts) == 0 {
		cloned.StatusCounts = nil
		return cloned
	}
	cloned.StatusCounts = make(map[JobStatus]int, len(snapshot.StatusCounts))
	for status, count := range snapshot.StatusCounts {
		cloned.StatusCounts[status] = count
	}
	return cloned
}

func persistJob(ctx context.Context, store jobQueueStore, job Job) error {
	if store == nil {
		return nil
	}
	if err := store.SaveJob(ctx, job); err != nil {
		return fmt.Errorf("persist job %q: %w", job.ID, err)
	}
	return nil
}

func persistAlert(ctx context.Context, sink AlertSink, alert AlertRecord) error {
	if sink == nil {
		return nil
	}
	if err := sink.RecordAlert(ctx, alert); err != nil {
		return fmt.Errorf("persist alert %q: %w", alert.ID, err)
	}
	return nil
}

func (q *JobQueue) persistDeadLetter(ctx context.Context, job Job) error {
	alert := jobDeadLetterAlert(job)
	if sink, ok := q.alerts.(deadLetterTransactionalAlertSink); ok {
		if err := sink.PersistJobDeadLetter(ctx, job, alert); err != nil {
			return fmt.Errorf("persist dead-letter job %q with alert: %w", job.ID, err)
		}
		return nil
	}
	if err := persistJob(ctx, q.store, job); err != nil {
		return err
	}
	if err := persistAlert(ctx, q.alerts, alert); err != nil {
		return err
	}
	return nil
}

func (q *JobQueue) persistDeadLetterRetry(ctx context.Context, job Job) error {
	alertID := deadLetterAlertID(job.ID)
	if sink, ok := q.alerts.(deadLetterRetryTransactionalAlertSink); ok {
		if err := sink.PersistJobDeadLetterRetry(ctx, job, alertID); err != nil {
			return fmt.Errorf("persist retried dead-letter job %q with alert resolution: %w", job.ID, err)
		}
		return nil
	}
	if err := persistJob(ctx, q.store, job); err != nil {
		return err
	}
	if resolver, ok := q.alerts.(alertResolver); ok {
		if err := resolver.DeleteAlert(ctx, alertID); err != nil {
			return fmt.Errorf("resolve alert %q: %w", alertID, err)
		}
	}
	return nil
}

func reviveDeadLetterJob(job Job) Job {
	updated := job
	updated.Status = JobStatusPending
	updated.LastError = ""
	updated.StartedAt = nil
	updated.FinishedAt = nil
	updated.NextRunAt = nil
	updated.DeadLetter = false
	return updated
}

func (q *JobQueue) syncMetricsLocked() {
	if q.metrics == nil {
		return
	}
	lag := 0
	counts := map[JobStatus]int{
		JobStatusPending:   0,
		JobStatusRunning:   0,
		JobStatusRetrying:  0,
		JobStatusCancelled: 0,
		JobStatusFailed:    0,
		JobStatusDead:      0,
		JobStatusDone:      0,
	}
	for _, job := range q.jobs {
		counts[job.Status]++
		if job.Status != JobStatusDone && job.Status != JobStatusDead && job.Status != JobStatusCancelled {
			lag++
		}
	}
	q.metrics.SetQueueLag(lag)
	for status, count := range counts {
		q.metrics.SetJobStatusCount(status, count)
	}
}

func (q *JobQueue) observeLifecycle(message string, job Job) {
	ctx := LogContext{
		TraceID:       job.TraceID,
		EventID:       job.EventID,
		RunID:         job.RunID,
		CorrelationID: job.Correlation,
	}
	if q.logger != nil {
		_ = q.logger.Log("info", message, ctx, map[string]any{
			"job_id":      job.ID,
			"job_type":    job.Type,
			"job_status":  job.Status,
			"retry_count": job.RetryCount,
			"dead_letter": job.DeadLetter,
		})
	}
	if q.tracer != nil {
		finish := q.tracer.StartSpan(ctx.TraceID, "job.lifecycle", ctx.EventID, "", "", ctx.CorrelationID, map[string]any{
			"job_id":     job.ID,
			"job_type":   job.Type,
			"job_status": job.Status,
		})
		finish()
	}
}

func jobLifecycleMessage(status JobStatus) string {
	switch status {
	case JobStatusRunning:
		return "job.running"
	case JobStatusDone:
		return "job.completed"
	case JobStatusCancelled:
		return "job.cancelled"
	default:
		return "job.updated"
	}
}

func simpleBackoff(retryCount int) time.Duration {
	if retryCount < 1 {
		retryCount = 1
	}
	return time.Duration(retryCount) * time.Second
}
