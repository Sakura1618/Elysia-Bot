package runtimecore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type failingJobQueueStore struct {
	err error
}

type recordingAlertSink struct {
	alerts []AlertRecord
	err    error
}

type failingQueuedJobDispatcher struct {
	err error
}

func (s *recordingAlertSink) RecordAlert(_ context.Context, alert AlertRecord) error {
	if s.err != nil {
		return s.err
	}
	s.alerts = append(s.alerts, alert)
	return nil
}

func (d failingQueuedJobDispatcher) DispatchQueuedJob(context.Context, Job) error {
	return d.err
}

func (s failingJobQueueStore) SaveJob(context.Context, Job) error {
	return s.err
}

func (s failingJobQueueStore) ListJobs(context.Context) ([]Job, error) {
	return nil, s.err
}

func TestJobQueueEnqueueInspectAndComplete(t *testing.T) {
	t.Parallel()

	queue := NewJobQueue()
	queue.SetWorkerIdentity("runtime-local:test-worker")
	queue.now = func() time.Time { return time.Date(2026, 4, 2, 20, 0, 0, 0, time.UTC) }

	job := NewJob("job-q-1", "ai.call", 1, 30*time.Second)
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	inspected, err := queue.Inspect(context.Background(), "job-q-1")
	if err != nil {
		t.Fatalf("inspect job: %v", err)
	}
	if inspected.Status != JobStatusPending {
		t.Fatalf("expected pending job, got %+v", inspected)
	}

	if _, err := queue.MarkRunning(context.Background(), "job-q-1"); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	running, err := queue.Inspect(context.Background(), "job-q-1")
	if err != nil {
		t.Fatalf("inspect running job: %v", err)
	}
	if running.WorkerID != "runtime-local:test-worker" || running.LeaseAcquiredAt == nil || running.LeaseExpiresAt == nil || running.HeartbeatAt == nil {
		t.Fatalf("expected running job ownership facts, got %+v", running)
	}
	completed, err := queue.Complete(context.Background(), "job-q-1")
	if err != nil {
		t.Fatalf("complete job: %v", err)
	}
	if completed.Status != JobStatusDone {
		t.Fatalf("expected done job, got %+v", completed)
	}
	if completed.WorkerID != "" || completed.LeaseAcquiredAt != nil || completed.HeartbeatAt != nil {
		t.Fatalf("expected ownership facts to clear on completion, got %+v", completed)
	}
}

func TestJobQueueFailureTriggersRetryWithBackoff(t *testing.T) {
	t.Parallel()

	queue := NewJobQueue()
	queue.SetWorkerIdentity("runtime-local:test-worker")
	queue.now = func() time.Time { return time.Date(2026, 4, 2, 20, 0, 0, 0, time.UTC) }

	job := NewJob("job-q-2", "file.process", 2, time.Minute)
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if _, err := queue.MarkRunning(context.Background(), "job-q-2"); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	retrying, err := queue.Fail(context.Background(), "job-q-2", "boom")
	if err != nil {
		t.Fatalf("fail job: %v", err)
	}
	if retrying.Status != JobStatusRetrying || retrying.RetryCount != 1 {
		t.Fatalf("expected retrying job, got %+v", retrying)
	}
	if retrying.ReasonCode != JobReasonCodeExecutionRetry {
		t.Fatalf("expected execution retry reason code, got %+v", retrying)
	}
	if retrying.NextRunAt == nil || !retrying.NextRunAt.Equal(time.Date(2026, 4, 2, 20, 0, 1, 0, time.UTC)) {
		t.Fatalf("unexpected backoff timestamp %+v", retrying.NextRunAt)
	}

	runningAgain, err := queue.Retry(context.Background(), "job-q-2")
	if err != nil {
		t.Fatalf("retry job: %v", err)
	}
	if runningAgain.Status != JobStatusRunning {
		t.Fatalf("expected running after retry, got %+v", runningAgain)
	}
}

func TestJobQueueFailureCanEnterDeadLetter(t *testing.T) {
	t.Parallel()

	queue := NewJobQueue()
	queue.SetWorkerIdentity("runtime-local:test-worker")
	queue.now = func() time.Time { return time.Date(2026, 4, 2, 20, 0, 0, 0, time.UTC) }

	job := NewJob("job-q-3", "webhook.retry", 0, time.Minute)
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if _, err := queue.MarkRunning(context.Background(), "job-q-3"); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	dead, err := queue.Timeout(context.Background(), "job-q-3")
	if err != nil {
		t.Fatalf("timeout job: %v", err)
	}
	if dead.Status != JobStatusDead || !dead.DeadLetter {
		t.Fatalf("expected dead-letter job, got %+v", dead)
	}
	if dead.ReasonCode != JobReasonCodeTimeout {
		t.Fatalf("expected timeout reason code, got %+v", dead)
	}
	if len(queue.DeadLetter(context.Background())) != 1 {
		t.Fatal("expected dead letter queue to contain one job")
	}
}

func TestJobQueueHeartbeatRenewsLeaseWithoutResettingLeaseAcquiredAt(t *testing.T) {
	t.Parallel()

	queue := NewJobQueue()
	queue.SetWorkerIdentity("runtime-local:test-worker")
	base := time.Date(2026, 4, 2, 20, 0, 0, 0, time.UTC)
	queue.now = func() time.Time { return base }

	job := NewJob("job-heartbeat-renew", "ai.chat", 1, 30*time.Second)
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if _, err := queue.MarkRunning(context.Background(), job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	first, err := queue.Inspect(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("inspect first running job: %v", err)
	}
	queue.now = func() time.Time { return base.Add(5 * time.Second) }
	renewed, err := queue.Heartbeat(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("heartbeat job: %v", err)
	}
	if renewed.LeaseAcquiredAt == nil || first.LeaseAcquiredAt == nil || !renewed.LeaseAcquiredAt.Equal(*first.LeaseAcquiredAt) {
		t.Fatalf("expected heartbeat to preserve lease acquisition time, got first=%+v renewed=%+v", first, renewed)
	}
	if renewed.HeartbeatAt == nil || !renewed.HeartbeatAt.Equal(base.Add(5*time.Second)) {
		t.Fatalf("expected heartbeat timestamp to move forward, got %+v", renewed)
	}
	if renewed.LeaseExpiresAt == nil || !renewed.LeaseExpiresAt.Equal(base.Add(35*time.Second)) {
		t.Fatalf("expected lease expiry to extend from renewed heartbeat, got %+v", renewed)
	}
}

func TestJobQueueDeadLetterTransitionEmitsSingleAlert(t *testing.T) {
	t.Parallel()

	queue := NewJobQueue()
	sink := &recordingAlertSink{}
	queue.SetAlertSink(sink)
	deadAt := time.Date(2026, 4, 2, 20, 30, 0, 0, time.UTC)
	queue.now = func() time.Time { return deadAt }

	job := NewJob("job-alert-1", "webhook.retry", 0, time.Minute)
	job.TraceID = "trace-alert-1"
	job.EventID = "evt-alert-1"
	job.RunID = "run-alert-1"
	job.Correlation = "corr-alert-1"
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if _, err := queue.MarkRunning(context.Background(), job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	dead, err := queue.Timeout(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("timeout job: %v", err)
	}
	if dead.Status != JobStatusDead || !dead.DeadLetter {
		t.Fatalf("expected dead-letter job, got %+v", dead)
	}
	if len(sink.alerts) != 1 {
		t.Fatalf("expected one alert, got %+v", sink.alerts)
	}
	alert := sink.alerts[0]
	if alert.ObjectID != job.ID || alert.ObjectType != alertObjectTypeJob || alert.FailureType != alertFailureTypeJobDeadLetter {
		t.Fatalf("expected dead-letter alert identity, got %+v", alert)
	}
	if !alert.FirstOccurredAt.Equal(deadAt) || !alert.LatestOccurredAt.Equal(deadAt) {
		t.Fatalf("expected dead-letter timestamps to match transition time, got %+v", alert)
	}
	if alert.LatestReason != "timeout" || alert.TraceID != job.TraceID || alert.EventID != job.EventID || alert.RunID != job.RunID || alert.Correlation != job.Correlation {
		t.Fatalf("expected dead-letter alert payload to carry latest reason and trace identifiers, got %+v", alert)
	}
}

func TestJobQueueDuplicateDeadLetterTransitionDoesNotDuplicateAlert(t *testing.T) {
	t.Parallel()

	queue := NewJobQueue()
	sink := &recordingAlertSink{}
	queue.SetAlertSink(sink)
	queue.now = func() time.Time { return time.Date(2026, 4, 2, 20, 45, 0, 0, time.UTC) }

	job := NewJob("job-alert-duplicate", "webhook.retry", 0, time.Minute)
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if _, err := queue.MarkRunning(context.Background(), job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if _, err := queue.Timeout(context.Background(), job.ID); err != nil {
		t.Fatalf("first timeout job: %v", err)
	}
	if _, err := queue.Timeout(context.Background(), job.ID); err == nil {
		t.Fatal("expected duplicate dead-letter transition to fail")
	}
	if len(sink.alerts) != 1 {
		t.Fatalf("expected duplicate dead-letter transition not to duplicate alert, got %+v", sink.alerts)
	}
}

func TestJobQueueRetryDeadLetterRevivesJobAndClearsAlert(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	queue := NewJobQueue()
	queue.SetStore(store)
	queue.SetAlertSink(store)
	queue.now = func() time.Time { return time.Date(2026, 4, 8, 9, 0, 0, 0, time.UTC) }

	job := NewJob("job-dead-retry", "ai.chat", 0, 30*time.Second)
	job.Correlation = "corr-dead-retry"
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if _, err := queue.MarkRunning(context.Background(), job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if _, err := queue.Timeout(context.Background(), job.ID); err != nil {
		t.Fatalf("timeout job: %v", err)
	}

	retried, err := queue.RetryDeadLetter(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("retry dead-letter job: %v", err)
	}
	if retried.Status != JobStatusPending || retried.DeadLetter || retried.FinishedAt != nil || retried.NextRunAt != nil {
		t.Fatalf("expected revived pending job without dead-letter markers, got %+v", retried)
	}
	if retried.RetryCount != 0 {
		t.Fatalf("expected retry count to be preserved on manual dead-letter retry, got %+v", retried)
	}
	if got := len(queue.DeadLetter(context.Background())); got != 0 {
		t.Fatalf("expected in-memory dead-letter list to clear after retry, got %d", got)
	}

	persisted, err := store.LoadJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("load retried job: %v", err)
	}
	if persisted.Status != JobStatusPending || persisted.DeadLetter || persisted.FinishedAt != nil {
		t.Fatalf("expected persisted revived pending job, got %+v", persisted)
	}
	alerts, err := store.ListAlerts(context.Background())
	if err != nil {
		t.Fatalf("list alerts: %v", err)
	}
	if len(alerts) != 0 {
		t.Fatalf("expected dead-letter alert to be resolved after retry, got %+v", alerts)
	}
	counts, err := store.Counts(context.Background())
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts["alerts"] != 0 {
		t.Fatalf("expected persisted alert count to be cleared after retry, got %+v", counts)
	}
	metricsOutput := queue.metrics.RenderPrometheus()
	if !strings.Contains(metricsOutput, `bot_platform_job_count{status="pending"} 1`) || !strings.Contains(metricsOutput, `bot_platform_job_count{status="dead"} 0`) {
		t.Fatalf("expected metrics to reflect revived pending job, got %s", metricsOutput)
	}
}

func TestJobQueueRetryDeadLetterRejectsNonDeadLetterJobs(t *testing.T) {
	t.Parallel()

	queue := NewJobQueue()
	job := NewJob("job-not-dead", "ai.chat", 1, 30*time.Second)
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	if _, err := queue.RetryDeadLetter(context.Background(), job.ID); err == nil || !strings.Contains(err.Error(), "is not dead-lettered") {
		t.Fatalf("expected non-dead-letter retry to be rejected safely, got %v", err)
	}
	stored, err := queue.Inspect(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("inspect job: %v", err)
	}
	if stored.Status != JobStatusPending || stored.DeadLetter {
		t.Fatalf("expected rejected retry not to mutate job, got %+v", stored)
	}
}

func TestJobQueueRecordsObservabilityAcrossLifecycle(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	logger := NewLogger(buffer)
	tracer := NewTraceRecorder()
	metrics := NewMetricsRegistry()
	queue := NewJobQueue()
	queue.SetObservability(logger, tracer, metrics)
	queue.now = func() time.Time { return time.Date(2026, 4, 2, 21, 0, 0, 0, time.UTC) }

	job := NewJob("job-observe", "ai.call", 0, 30*time.Second)
	job.TraceID = "trace-job"
	job.EventID = "evt-job"
	job.Correlation = "corr-job"
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if _, err := queue.MarkRunning(context.Background(), job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if _, err := queue.Complete(context.Background(), job.ID); err != nil {
		t.Fatalf("complete job: %v", err)
	}

	logs := buffer.String()
	for _, expected := range []string{"job.enqueued", "job.running", "job.completed", "trace-job", "evt-job", "corr-job"} {
		if !strings.Contains(logs, expected) {
			t.Fatalf("expected lifecycle logs to contain %q, got %s", expected, logs)
		}
	}
	if rendered := tracer.RenderTrace("trace-job"); !strings.Contains(rendered, "job.lifecycle") {
		t.Fatalf("expected job lifecycle span, got %s", rendered)
	}
	metricsOutput := metrics.RenderPrometheus()
	if !strings.Contains(metricsOutput, "bot_platform_job_queue_active_count 0") || !strings.Contains(metricsOutput, "bot_platform_job_count{status=\"done\"} 1") {
		t.Fatalf("expected job metrics, got %s", metricsOutput)
	}
}

func TestJobQueueDispatchReadyLogsStructuredDispatcherFailure(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	queue := NewJobQueue()
	queue.SetObservability(NewLogger(buffer), NewTraceRecorder(), NewMetricsRegistry())
	queue.SetWorkerIdentity("runtime-local:test-worker")
	queue.now = func() time.Time { return time.Date(2026, 4, 2, 21, 15, 0, 0, time.UTC) }
	if err := queue.RegisterDispatcher("ai.call", failingQueuedJobDispatcher{err: errors.New("dispatcher exploded")}); err != nil {
		t.Fatalf("register dispatcher: %v", err)
	}
	job := NewJob("job-dispatch-ready-fail", "ai.call", 0, 30*time.Second)
	job.TraceID = "trace-dispatch-ready-fail"
	job.EventID = "evt-dispatch-ready-fail"
	job.Correlation = "corr-dispatch-ready-fail"
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	queue.DispatchReady(context.Background(), time.Date(2026, 4, 2, 21, 15, 1, 0, time.UTC))

	entries := decodeJobQueueLogEntries(t, buffer)
	matched := false
	for _, entry := range entries {
		if entry.Message != "job queue dispatcher returned error" {
			continue
		}
		matched = true
		if entry.Fields["component"] != "job_queue" || entry.Fields["operation"] != "dispatch_ready.dispatch" {
			t.Fatalf("expected queue failure baseline fields, got %+v", entry)
		}
		if entry.Fields["error_category"] != "internal" || entry.Fields["error_code"] != "queued_dispatch_failed" {
			t.Fatalf("expected queue failure taxonomy, got %+v", entry)
		}
	}
	if !matched {
		t.Fatalf("expected queue dispatcher failure log entry, got %+v", entries)
	}
}

func decodeJobQueueLogEntries(t *testing.T, buffer *bytes.Buffer) []LogEntry {
	t.Helper()
	raw := strings.TrimSpace(buffer.String())
	if raw == "" {
		return nil
	}
	lines := strings.Split(raw, "\n")
	entries := make([]LogEntry, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode job queue log entry %q: %v", line, err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestJobQueueCanCancelPendingAndRetryingJobs(t *testing.T) {
	t.Parallel()

	queue := NewJobQueue()
	queue.now = func() time.Time { return time.Date(2026, 4, 2, 22, 0, 0, 0, time.UTC) }

	pending := NewJob("job-q-cancel-pending", "ai.call", 1, 30*time.Second)
	if err := queue.Enqueue(context.Background(), pending); err != nil {
		t.Fatalf("enqueue pending job: %v", err)
	}
	cancelledPending, err := queue.Cancel(context.Background(), pending.ID)
	if err != nil {
		t.Fatalf("cancel pending job: %v", err)
	}
	if cancelledPending.Status != JobStatusCancelled || cancelledPending.FinishedAt == nil {
		t.Fatalf("expected cancelled pending job, got %+v", cancelledPending)
	}

	retrying := NewJob("job-q-cancel-retrying", "ai.call", 2, 30*time.Second)
	if err := queue.Enqueue(context.Background(), retrying); err != nil {
		t.Fatalf("enqueue retrying job: %v", err)
	}
	if _, err := queue.MarkRunning(context.Background(), retrying.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if _, err := queue.Fail(context.Background(), retrying.ID, "boom"); err != nil {
		t.Fatalf("fail job into retrying: %v", err)
	}
	cancelledRetrying, err := queue.Cancel(context.Background(), retrying.ID)
	if err != nil {
		t.Fatalf("cancel retrying job: %v", err)
	}
	if cancelledRetrying.Status != JobStatusCancelled || cancelledRetrying.NextRunAt != nil {
		t.Fatalf("expected cancelled retrying job, got %+v", cancelledRetrying)
	}
	metricsOutput := queue.metrics.RenderPrometheus()
	if !strings.Contains(metricsOutput, "bot_platform_job_count{status=\"cancelled\"} 2") || !strings.Contains(metricsOutput, "bot_platform_job_queue_active_count 0") {
		t.Fatalf("expected cancelled job metrics, got %s", metricsOutput)
	}
}

func TestJobQueueRejectsCancelForRunningAndTerminalJobs(t *testing.T) {
	t.Parallel()

	queue := NewJobQueue()
	running := NewJob("job-q-running", "ai.call", 1, 30*time.Second)
	if err := queue.Enqueue(context.Background(), running); err != nil {
		t.Fatalf("enqueue running job: %v", err)
	}
	if _, err := queue.MarkRunning(context.Background(), running.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if _, err := queue.Cancel(context.Background(), running.ID); err == nil {
		t.Fatal("expected running job cancel to fail")
	}

	done := NewJob("job-q-done", "ai.call", 0, 30*time.Second)
	if err := queue.Enqueue(context.Background(), done); err != nil {
		t.Fatalf("enqueue done job: %v", err)
	}
	if _, err := queue.MarkRunning(context.Background(), done.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if _, err := queue.Complete(context.Background(), done.ID); err != nil {
		t.Fatalf("complete job: %v", err)
	}
	if _, err := queue.Cancel(context.Background(), done.ID); err == nil {
		t.Fatal("expected done job cancel to fail")
	}

	dead := NewJob("job-q-dead", "ai.call", 0, 30*time.Second)
	if err := queue.Enqueue(context.Background(), dead); err != nil {
		t.Fatalf("enqueue dead job: %v", err)
	}
	if _, err := queue.MarkRunning(context.Background(), dead.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if _, err := queue.Timeout(context.Background(), dead.ID); err != nil {
		t.Fatalf("timeout dead job: %v", err)
	}
	if _, err := queue.Cancel(context.Background(), dead.ID); err == nil {
		t.Fatal("expected dead job cancel to fail")
	}

	failed := Job{ID: "job-q-failed", Type: "ai.call", Status: JobStatusFailed, MaxRetries: 1, Timeout: 30 * time.Second, CreatedAt: time.Now().UTC()}
	queue.jobs[failed.ID] = failed
	if _, err := queue.Cancel(context.Background(), failed.ID); err == nil {
		t.Fatal("expected failed job cancel to fail")
	}
}

func TestJobQueuePersistsEnqueueToSQLiteStore(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	queue := NewJobQueue()
	queue.SetStore(store)
	queue.now = func() time.Time { return time.Date(2026, 4, 7, 11, 0, 0, 0, time.UTC) }

	job := NewJob("job-persist-enqueue", "ai.chat", 1, 30*time.Second)
	job.TraceID = "trace-persist-enqueue"
	job.EventID = "evt-persist-enqueue"
	job.Correlation = "corr-persist-enqueue"
	job.Payload = map[string]any{"prompt": "hello"}
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	stored, err := store.LoadJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("load job from store: %v", err)
	}
	if stored.ID != job.ID || stored.Status != JobStatusPending || stored.TraceID != job.TraceID {
		t.Fatalf("expected persisted pending job, got %+v", stored)
	}
}

func TestJobQueuePersistsLifecycleTransitionsToSQLiteStore(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	queue := NewJobQueue()
	queue.SetStore(store)
	queue.now = func() time.Time { return time.Date(2026, 4, 7, 11, 5, 0, 0, time.UTC) }

	job := NewJob("job-persist-lifecycle", "ai.chat", 1, 30*time.Second)
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if _, err := queue.MarkRunning(context.Background(), job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if _, err := queue.Fail(context.Background(), job.ID, "mock upstream failure"); err != nil {
		t.Fatalf("fail job: %v", err)
	}

	stored, err := store.LoadJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("load job from store: %v", err)
	}
	if stored.Status != JobStatusRetrying || stored.RetryCount != 1 || stored.LastError != "mock upstream failure" {
		t.Fatalf("expected persisted retrying job, got %+v", stored)
	}
	if stored.NextRunAt == nil {
		t.Fatalf("expected persisted next run timestamp, got %+v", stored)
	}
}

func TestJobQueueRestoreKeepsPendingAndRetryingJobsRunnable(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	pending := NewJob("job-restore-pending", "ai.chat", 1, 30*time.Second)
	retrying := Job{
		ID:         "job-restore-retrying",
		Type:       "ai.chat",
		Status:     JobStatusRetrying,
		RetryCount: 1,
		MaxRetries: 2,
		Timeout:    30 * time.Second,
		CreatedAt:  time.Date(2026, 4, 7, 11, 10, 0, 0, time.UTC),
	}
	nextRunAt := time.Date(2026, 4, 7, 11, 10, 3, 0, time.UTC)
	retrying.NextRunAt = &nextRunAt
	if err := store.SaveJob(context.Background(), pending); err != nil {
		t.Fatalf("save pending job: %v", err)
	}
	if err := store.SaveJob(context.Background(), retrying); err != nil {
		t.Fatalf("save retrying job: %v", err)
	}

	queue := NewJobQueue()
	queue.SetStore(store)
	queue.now = func() time.Time { return time.Date(2026, 4, 7, 11, 11, 0, 0, time.UTC) }
	if err := queue.Restore(context.Background()); err != nil {
		t.Fatalf("restore queue: %v", err)
	}

	restoredPending, err := queue.Inspect(context.Background(), pending.ID)
	if err != nil {
		t.Fatalf("inspect pending job: %v", err)
	}
	if restoredPending.Status != JobStatusPending {
		t.Fatalf("expected pending job to stay pending, got %+v", restoredPending)
	}
	restoredRetrying, err := queue.Inspect(context.Background(), retrying.ID)
	if err != nil {
		t.Fatalf("inspect retrying job: %v", err)
	}
	if restoredRetrying.Status != JobStatusRetrying || restoredRetrying.NextRunAt == nil || !restoredRetrying.NextRunAt.Equal(nextRunAt) {
		t.Fatalf("expected retrying job to keep status and next run, got %+v", restoredRetrying)
	}
}

func TestJobQueueRestoreConvertsRunningJobToRetryingOrDead(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	runningRetry := Job{
		ID:         "job-restore-running-retry",
		Type:       "ai.chat",
		Status:     JobStatusRunning,
		RetryCount: 0,
		MaxRetries: 1,
		Timeout:    30 * time.Second,
		CreatedAt:  time.Date(2026, 4, 7, 11, 20, 0, 0, time.UTC),
	}
	leaseAcquiredAt := time.Date(2026, 4, 7, 11, 20, 0, 0, time.UTC)
	heartbeatAt := leaseAcquiredAt.Add(5 * time.Second)
	leaseExpiresAt := heartbeatAt.Add(-1 * time.Second)
	runningRetry.WorkerID = "runtime-local:test-worker"
	runningRetry.LeaseAcquiredAt = &leaseAcquiredAt
	runningRetry.LeaseExpiresAt = &leaseExpiresAt
	runningRetry.HeartbeatAt = &heartbeatAt
	runningDead := Job{
		ID:         "job-restore-running-dead",
		Type:       "ai.chat",
		Status:     JobStatusRunning,
		RetryCount: 0,
		MaxRetries: 0,
		Timeout:    30 * time.Second,
		CreatedAt:  time.Date(2026, 4, 7, 11, 20, 1, 0, time.UTC),
	}
	runningDead.WorkerID = "runtime-local:test-worker"
	runningDead.LeaseAcquiredAt = &leaseAcquiredAt
	runningDead.LeaseExpiresAt = &leaseExpiresAt
	runningDead.HeartbeatAt = &heartbeatAt
	if err := store.SaveJob(context.Background(), runningRetry); err != nil {
		t.Fatalf("save running retry job: %v", err)
	}
	if err := store.SaveJob(context.Background(), runningDead); err != nil {
		t.Fatalf("save running dead job: %v", err)
	}

	recoveryAt := time.Date(2026, 4, 7, 11, 21, 0, 0, time.UTC)
	queue := NewJobQueue()
	queue.SetStore(store)
	queue.SetAlertSink(store)
	queue.now = func() time.Time { return recoveryAt }
	if err := queue.Restore(context.Background()); err != nil {
		t.Fatalf("restore queue: %v", err)
	}

	retried, err := queue.Inspect(context.Background(), runningRetry.ID)
	if err != nil {
		t.Fatalf("inspect recovered retry job: %v", err)
	}
	if retried.Status != JobStatusRetrying || retried.RetryCount != 1 || retried.NextRunAt == nil || !retried.NextRunAt.Equal(recoveryAt) {
		t.Fatalf("expected running job to recover as retrying, got %+v", retried)
	}
	if retried.ReasonCode != JobReasonCodeWorkerAbandoned || !strings.Contains(retried.LastError, "lease abandoned") {
		t.Fatalf("expected restart error reason, got %+v", retried)
	}

	dead, err := queue.Inspect(context.Background(), runningDead.ID)
	if err != nil {
		t.Fatalf("inspect recovered dead job: %v", err)
	}
	if dead.Status != JobStatusDead || !dead.DeadLetter || dead.FinishedAt == nil || !dead.FinishedAt.Equal(recoveryAt) {
		t.Fatalf("expected running job with no retries to recover as dead, got %+v", dead)
	}

	persistedDead, err := store.LoadJob(context.Background(), runningDead.ID)
	if err != nil {
		t.Fatalf("load dead job from store: %v", err)
	}
	if persistedDead.Status != JobStatusDead {
		t.Fatalf("expected recovered dead job to persist, got %+v", persistedDead)
	}
	snapshot := queue.LastRecoverySnapshot()
	if snapshot.TotalJobs != 2 || snapshot.RecoveredJobs != 2 || snapshot.RecoveredRunning != 2 {
		t.Fatalf("expected recovery snapshot to count recovered running jobs, got %+v", snapshot)
	}
	if snapshot.RetriedJobs != 1 || snapshot.DeadJobs != 1 {
		t.Fatalf("expected recovery snapshot retry/dead counts, got %+v", snapshot)
	}
	if snapshot.StatusCounts[JobStatusRetrying] != 1 || snapshot.StatusCounts[JobStatusDead] != 1 {
		t.Fatalf("expected recovery snapshot status counts, got %+v", snapshot.StatusCounts)
	}
	alerts, err := store.ListAlerts(context.Background())
	if err != nil {
		t.Fatalf("list alerts: %v", err)
	}
	if len(alerts) != 1 || alerts[0].ObjectID != runningDead.ID || !strings.Contains(alerts[0].LatestReason, "lease abandoned") {
		t.Fatalf("expected one persisted dead-letter alert after restore, got %+v", alerts)
	}
}

func TestJobQueueRestoreKeepsRuntimeRestartReasonWhenLeaseIsStillValid(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	leaseAcquiredAt := time.Date(2026, 4, 7, 11, 20, 0, 0, time.UTC)
	heartbeatAt := leaseAcquiredAt.Add(5 * time.Second)
	leaseExpiresAt := heartbeatAt.Add(30 * time.Second)
	running := Job{
		ID:              "job-restore-runtime-restart",
		Type:            "ai.chat",
		Status:          JobStatusRunning,
		RetryCount:      0,
		MaxRetries:      1,
		Timeout:         30 * time.Second,
		CreatedAt:       time.Date(2026, 4, 7, 11, 20, 0, 0, time.UTC),
		WorkerID:        "runtime-local:test-worker",
		LeaseAcquiredAt: &leaseAcquiredAt,
		LeaseExpiresAt:  &leaseExpiresAt,
		HeartbeatAt:     &heartbeatAt,
	}
	if err := store.SaveJob(context.Background(), running); err != nil {
		t.Fatalf("save running job: %v", err)
	}

	queue := NewJobQueue()
	queue.SetStore(store)
	queue.now = func() time.Time { return heartbeatAt.Add(10 * time.Second) }
	if err := queue.Restore(context.Background()); err != nil {
		t.Fatalf("restore queue: %v", err)
	}

	restored, err := queue.Inspect(context.Background(), running.ID)
	if err != nil {
		t.Fatalf("inspect restored job: %v", err)
	}
	if restored.ReasonCode != JobReasonCodeRuntimeRestart || !strings.Contains(restored.LastError, "runtime restarted") {
		t.Fatalf("expected runtime restart classification for unexpired lease, got %+v", restored)
	}
}

func TestJobQueueReadyJobsIncludesPendingAndDueRetryingOnly(t *testing.T) {
	t.Parallel()

	queue := NewJobQueue()
	now := time.Date(2026, 4, 9, 14, 0, 0, 0, time.UTC)
	queue.now = func() time.Time { return now }

	pending := NewJob("job-ready-pending", "ai.chat", 1, 30*time.Second)
	pending.CreatedAt = now.Add(-3 * time.Second)
	if err := queue.Enqueue(context.Background(), pending); err != nil {
		t.Fatalf("enqueue pending job: %v", err)
	}

	retryingDue := NewJob("job-ready-retrying-due", "ai.chat", 2, 30*time.Second)
	retryingDue.CreatedAt = now.Add(-2 * time.Second)
	if err := queue.Enqueue(context.Background(), retryingDue); err != nil {
		t.Fatalf("enqueue retrying due job: %v", err)
	}
	if _, err := queue.MarkRunning(context.Background(), retryingDue.ID); err != nil {
		t.Fatalf("mark running retrying due job: %v", err)
	}
	queue.now = func() time.Time { return now.Add(-1 * time.Second) }
	if _, err := queue.Fail(context.Background(), retryingDue.ID, "boom"); err != nil {
		t.Fatalf("fail retrying due job: %v", err)
	}

	retryingFuture := NewJob("job-ready-retrying-future", "ai.chat", 2, 30*time.Second)
	retryingFuture.CreatedAt = now.Add(-1 * time.Second)
	if err := queue.Enqueue(context.Background(), retryingFuture); err != nil {
		t.Fatalf("enqueue retrying future job: %v", err)
	}
	if _, err := queue.MarkRunning(context.Background(), retryingFuture.ID); err != nil {
		t.Fatalf("mark running retrying future job: %v", err)
	}
	queue.now = func() time.Time { return now }
	if _, err := queue.Fail(context.Background(), retryingFuture.ID, "boom"); err != nil {
		t.Fatalf("fail retrying future job: %v", err)
	}

	done := NewJob("job-ready-done", "ai.chat", 0, 30*time.Second)
	done.CreatedAt = now
	if err := queue.Enqueue(context.Background(), done); err != nil {
		t.Fatalf("enqueue done job: %v", err)
	}
	if _, err := queue.MarkRunning(context.Background(), done.ID); err != nil {
		t.Fatalf("mark running done job: %v", err)
	}
	if _, err := queue.Complete(context.Background(), done.ID); err != nil {
		t.Fatalf("complete done job: %v", err)
	}

	ready := queue.ReadyJobs(now)
	if len(ready) != 2 {
		t.Fatalf("expected 2 ready jobs, got %+v", ready)
	}
	if ready[0].ID != pending.ID || ready[1].ID != retryingDue.ID {
		t.Fatalf("expected pending then due retrying jobs, got %+v", ready)
	}
}

func TestJobQueueEnqueueDoesNotMutateMemoryWhenPersistenceFails(t *testing.T) {
	t.Parallel()

	queue := NewJobQueue()
	queue.SetStore(failingJobQueueStore{err: errors.New("mock save failure")})

	job := NewJob("job-persist-fail-enqueue", "ai.chat", 1, 30*time.Second)
	err := queue.Enqueue(context.Background(), job)
	if err == nil || !strings.Contains(err.Error(), "mock save failure") {
		t.Fatalf("expected persistence failure, got %v", err)
	}
	if _, inspectErr := queue.Inspect(context.Background(), job.ID); inspectErr == nil {
		t.Fatalf("expected job not to exist in memory after failed enqueue")
	}
	if got := len(queue.List()); got != 0 {
		t.Fatalf("expected empty in-memory queue after failed enqueue, got %d jobs", got)
	}
	metricsOutput := queue.metrics.RenderPrometheus()
	if !strings.Contains(metricsOutput, "bot_platform_job_queue_active_count 0") {
		t.Fatalf("expected queue lag to remain zero, got %s", metricsOutput)
	}
}

func TestJobQueueTransitionDoesNotMutateMemoryWhenPersistenceFails(t *testing.T) {
	t.Parallel()

	queue := NewJobQueue()
	job := NewJob("job-persist-fail-transition", "ai.chat", 0, 30*time.Second)
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	queue.SetStore(failingJobQueueStore{err: errors.New("mock transition save failure")})

	updated, err := queue.MarkRunning(context.Background(), job.ID)
	if err == nil || !strings.Contains(err.Error(), "mock transition save failure") {
		t.Fatalf("expected persistence failure, got updated=%+v err=%v", updated, err)
	}

	stored, inspectErr := queue.Inspect(context.Background(), job.ID)
	if inspectErr != nil {
		t.Fatalf("inspect job after failed transition: %v", inspectErr)
	}
	if stored.Status != JobStatusPending {
		t.Fatalf("expected job to remain pending after failed transition, got %+v", stored)
	}
	if got := len(queue.DeadLetter(context.Background())); got != 0 {
		t.Fatalf("expected no dead-letter entries after failed transition, got %d", got)
	}
	metricsOutput := queue.metrics.RenderPrometheus()
	if !strings.Contains(metricsOutput, "bot_platform_job_count{status=\"pending\"} 1") {
		t.Fatalf("expected pending job metric to remain unchanged, got %s", metricsOutput)
	}
}
