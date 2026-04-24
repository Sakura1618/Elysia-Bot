package faultinjection

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adapteronebot "github.com/ohmyopencode/bot-platform/adapters/adapter-onebot"
	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
	pluginaichat "github.com/ohmyopencode/bot-platform/plugins/plugin-ai-chat"
)

type failingHost struct{}

type noopEventHandler struct{}

type timeoutProviderStub struct{}

type successProviderStub struct {
	response string
}

type faultSessionRecorder struct{}

type faultReplyRecorder struct {
	text string
}

type oneBotReplyServiceBridge struct {
	sender *adapteronebot.OneBotReplySender
}

type faultJobQueueStub struct {
	jobs map[string]pluginsdk.Job
	now  func() time.Time
}

func (failingHost) DispatchEvent(ctx context.Context, plugin pluginsdk.Plugin, event eventmodel.Event, executionContext eventmodel.ExecutionContext) error {
	return errors.New("plugin crashed")
}

func (failingHost) DispatchCommand(ctx context.Context, plugin pluginsdk.Plugin, command eventmodel.CommandInvocation, executionContext eventmodel.ExecutionContext) error {
	return errors.New("plugin crashed")
}

func (failingHost) DispatchJob(ctx context.Context, plugin pluginsdk.Plugin, job pluginsdk.JobInvocation, executionContext eventmodel.ExecutionContext) error {
	return errors.New("plugin crashed")
}

func (failingHost) DispatchSchedule(ctx context.Context, plugin pluginsdk.Plugin, trigger pluginsdk.ScheduleTrigger, executionContext eventmodel.ExecutionContext) error {
	return errors.New("plugin crashed")
}

func (noopEventHandler) OnEvent(event eventmodel.Event, ctx eventmodel.ExecutionContext) error {
	return nil
}

func (timeoutProviderStub) Generate(ctx context.Context, prompt string) (string, error) {
	return "", errors.New("upstream timeout")
}

func (p successProviderStub) Generate(ctx context.Context, prompt string) (string, error) {
	return p.response, nil
}

func (faultSessionRecorder) SaveSession(ctx context.Context, session pluginsdk.SessionState) error {
	return nil
}

func (r *faultReplyRecorder) ReplyText(handle eventmodel.ReplyHandle, text string) error {
	r.text = text
	return nil
}

func (r *faultReplyRecorder) ReplyImage(handle eventmodel.ReplyHandle, imageURL string) error {
	return nil
}
func (r *faultReplyRecorder) ReplyFile(handle eventmodel.ReplyHandle, fileURL string) error {
	return nil
}

func (b oneBotReplyServiceBridge) ReplyText(handle eventmodel.ReplyHandle, text string) error {
	_, err := b.sender.ReplyText(handle, text)
	return err
}

func (b oneBotReplyServiceBridge) ReplyImage(handle eventmodel.ReplyHandle, imageURL string) error {
	_, err := b.sender.ReplyImage(handle, imageURL)
	return err
}

func (b oneBotReplyServiceBridge) ReplyFile(handle eventmodel.ReplyHandle, fileURL string) error {
	_, err := b.sender.ReplyFile(handle, fileURL)
	return err
}

func newFaultJobQueueStub() *faultJobQueueStub {
	return &faultJobQueueStub{jobs: map[string]pluginsdk.Job{}, now: func() time.Time { return time.Now().UTC() }}
}

func (q *faultJobQueueStub) Enqueue(_ context.Context, job pluginsdk.Job) error {
	if job.Status == "" {
		job.Status = pluginsdk.JobStatusPending
	}
	if err := job.Validate(); err != nil {
		return err
	}
	q.jobs[job.ID] = job
	return nil
}

func (q *faultJobQueueStub) MarkRunning(_ context.Context, id string) (pluginsdk.Job, error) {
	return q.transition(id, pluginsdk.JobStatusRunning, "")
}

func (q *faultJobQueueStub) Complete(_ context.Context, id string) (pluginsdk.Job, error) {
	return q.transition(id, pluginsdk.JobStatusDone, "")
}

func (q *faultJobQueueStub) Fail(_ context.Context, id string, reason string) (pluginsdk.Job, error) {
	job, exists := q.jobs[id]
	if !exists {
		return pluginsdk.Job{}, errors.New("job not found")
	}
	if job.RetryCount < job.MaxRetries {
		updated, err := job.Transition(pluginsdk.JobStatusRetrying, q.now().Add(time.Duration(job.RetryCount+1)*time.Second), reason)
		if err != nil {
			return pluginsdk.Job{}, err
		}
		q.jobs[id] = updated
		return updated, nil
	}
	updated, err := job.Transition(pluginsdk.JobStatusDead, q.now(), reason)
	if err != nil {
		return pluginsdk.Job{}, err
	}
	q.jobs[id] = updated
	return updated, nil
}

func (q *faultJobQueueStub) Inspect(_ context.Context, id string) (pluginsdk.Job, error) {
	job, exists := q.jobs[id]
	if !exists {
		return pluginsdk.Job{}, errors.New("job not found")
	}
	return job, nil
}

func (q *faultJobQueueStub) transition(id string, next pluginsdk.JobStatus, reason string) (pluginsdk.Job, error) {
	job, exists := q.jobs[id]
	if !exists {
		return pluginsdk.Job{}, errors.New("job not found")
	}
	updated, err := job.Transition(next, q.now(), reason)
	if err != nil {
		return pluginsdk.Job{}, err
	}
	q.jobs[id] = updated
	return updated, nil
}

func TestFaultInjectionPluginCrashDoesNotKillRuntime(t *testing.T) {
	t.Parallel()

	runtime := runtimecore.NewInMemoryRuntime(runtimecore.NoopSupervisor{}, failingHost{})
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			SchemaVersion: pluginsdk.SupportedPluginManifestSchemaVersion,
			ID:            "plugin-crash",
			Name:          "Crash Plugin",
			Version:       "0.1.0",
			APIVersion:    "v0",
			Mode:          pluginsdk.ModeSubprocess,
			Entry:         pluginsdk.PluginEntry{Module: "plugins/crash", Symbol: "Plugin"},
		},
		Handlers: pluginsdk.Handlers{Event: noopEventHandler{}},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	err := runtime.DispatchEvent(context.Background(), eventmodel.Event{
		EventID:        "evt-crash",
		TraceID:        "trace-crash",
		Source:         "scheduler",
		Type:           "schedule.triggered",
		Timestamp:      time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC),
		IdempotencyKey: "schedule:crash",
	})
	if err == nil {
		t.Fatal("expected dispatch to report no successful plugin deliveries")
	}
	if len(runtime.DispatchResults()) != 1 || runtime.DispatchResults()[0].Success {
		t.Fatalf("expected failed dispatch result, got %+v", runtime.DispatchResults())
	}
	if len(runtime.RegisteredAdapters()) != 0 {
		t.Fatalf("unexpected runtime corruption after plugin crash: %+v", runtime.RegisteredAdapters())
	}
}

func TestFaultInjectionTimeoutLeadsToRetryAndDeadLetter(t *testing.T) {
	t.Parallel()

	queue := runtimecore.NewJobQueue()
	job := runtimecore.NewJob("job-timeout", "external.api", 0, 5*time.Second)
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := queue.MarkRunning(context.Background(), job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	dead, err := queue.Timeout(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("timeout: %v", err)
	}
	if dead.Status != runtimecore.JobStatusDead || !dead.DeadLetter {
		t.Fatalf("expected dead-letter timeout result, got %+v", dead)
	}
}

func TestFaultInjectionAIProviderTimeoutReturnsFailureFeedbackAndDeadLettersJob(t *testing.T) {
	t.Parallel()

	queue := newFaultJobQueueStub()
	replies := &faultReplyRecorder{}
	plugin := pluginaichat.New(queue, timeoutProviderStub{}, faultSessionRecorder{}, replies)
	job := pluginsdk.NewJob("job-ai-timeout", "ai.chat", 0, 30*time.Second)
	job.Payload = map[string]any{"prompt": "hello ai"}
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue ai job: %v", err)
	}

	if err := plugin.ProcessJob(context.Background(), job.ID, eventmodel.ReplyHandle{Capability: "onebot.reply", TargetID: "group-42"}); err != nil {
		t.Fatalf("expected provider timeout feedback path to return nil after reply, got %v", err)
	}
	if replies.text != "AI request failed: upstream timeout" {
		t.Fatalf("unexpected timeout reply %q", replies.text)
	}
	stored, err := queue.Inspect(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("inspect ai job: %v", err)
	}
	if stored.Status != pluginsdk.JobStatusDead || !stored.DeadLetter || stored.LastError != "upstream timeout" {
		t.Fatalf("expected dead-lettered ai timeout job, got %+v", stored)
	}
}

func TestFaultInjectionOneBotOutboundHTTPFailurePropagatesThroughAIJob(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream unavailable"))
	}))
	defer server.Close()

	queue := newFaultJobQueueStub()
	replies := oneBotReplyServiceBridge{
		sender: adapteronebot.NewOneBotReplySender(adapteronebot.NewOneBotReplyHTTPTransport(server.URL, server.Client())),
	}
	plugin := pluginaichat.New(queue, successProviderStub{response: "ai response"}, faultSessionRecorder{}, replies)
	job := pluginsdk.NewJob("job-ai-onebot-http-failure", "ai.chat", 1, 30*time.Second)
	job.Payload = map[string]any{"prompt": "hello ai"}
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue ai job: %v", err)
	}

	err := plugin.ProcessJob(context.Background(), job.ID, eventmodel.ReplyHandle{
		Capability: "onebot.reply",
		TargetID:   "group-42",
		Metadata: map[string]any{
			"message_type": "group",
			"group_id":     int64(42),
		},
	})
	if err == nil {
		t.Fatal("expected outbound onebot reply http failure to bubble through ai job path")
	}

	var httpErr *adapteronebot.OneBotReplyHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected classified onebot http transport error, got %T (%v)", err, err)
	}
	if !httpErr.GenericUpstreamFailure() || !httpErr.UpstreamFailure() {
		t.Fatalf("expected 502 to retain upstream failure classification, got %+v", httpErr)
	}
	if httpErr.StatusCode != http.StatusBadGateway || httpErr.Body != "upstream unavailable" {
		t.Fatalf("expected 502 body details to survive ai job path, got %+v", httpErr)
	}

	stored, inspectErr := queue.Inspect(context.Background(), job.ID)
	if inspectErr != nil {
		t.Fatalf("inspect ai job: %v", inspectErr)
	}
	if stored.Status != pluginsdk.JobStatusRetrying {
		t.Fatalf("expected outbound reply failure to move ai job into retrying, got %+v", stored)
	}
	if !strings.Contains(stored.LastError, "502 Bad Gateway") || !strings.Contains(stored.LastError, "upstream unavailable") {
		t.Fatalf("expected job last error to preserve outbound http failure detail, got %+v", stored)
	}
}

func TestFaultInjectionOneBotOutboundRateLimitPropagatesThroughAIJob(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("slow down"))
	}))
	defer server.Close()

	queue := newFaultJobQueueStub()
	replies := oneBotReplyServiceBridge{
		sender: adapteronebot.NewOneBotReplySender(adapteronebot.NewOneBotReplyHTTPTransport(server.URL, server.Client())),
	}
	plugin := pluginaichat.New(queue, successProviderStub{response: "ai response"}, faultSessionRecorder{}, replies)
	job := pluginsdk.NewJob("job-ai-onebot-rate-limit", "ai.chat", 1, 30*time.Second)
	job.Payload = map[string]any{"prompt": "hello ai"}
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue ai job: %v", err)
	}

	err := plugin.ProcessJob(context.Background(), job.ID, eventmodel.ReplyHandle{
		Capability: "onebot.reply",
		TargetID:   "group-42",
		Metadata: map[string]any{
			"message_type": "group",
			"group_id":     int64(42),
		},
	})
	if err == nil {
		t.Fatal("expected outbound onebot reply 429 rate limit to bubble through ai job path")
	}

	var httpErr *adapteronebot.OneBotReplyHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected classified onebot http transport error, got %T (%v)", err, err)
	}
	if httpErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %+v", httpErr)
	}
	if httpErr.RetryAfter != "120" {
		t.Fatalf("expected retry-after hint to survive ai job path, got %+v", httpErr)
	}
	if !httpErr.RateLimited() {
		t.Fatalf("expected 429 to retain rate-limit classification, got %+v", httpErr)
	}
	if !httpErr.UpstreamFailure() {
		t.Fatalf("expected 429 to retain upstream failure classification, got %+v", httpErr)
	}
	if httpErr.GenericUpstreamFailure() {
		t.Fatalf("expected 429 to remain distinct from generic upstream failure, got %+v", httpErr)
	}

	stored, inspectErr := queue.Inspect(context.Background(), job.ID)
	if inspectErr != nil {
		t.Fatalf("inspect ai job: %v", inspectErr)
	}
	if stored.Status != pluginsdk.JobStatusRetrying {
		t.Fatalf("expected outbound reply rate limit to move ai job into retrying, got %+v", stored)
	}
	if !strings.Contains(stored.LastError, "429 Too Many Requests") || !strings.Contains(stored.LastError, "slow down") || !strings.Contains(stored.LastError, "retry-after: 120") {
		t.Fatalf("expected job last error to preserve outbound 429 detail, body, and retry-after hint, got %+v", stored)
	}
}

func TestFaultInjectionSQLiteDisconnectReturnsError(t *testing.T) {
	t.Parallel()

	store, err := runtimecore.OpenSQLiteStateStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close sqlite store: %v", err)
	}
	if err := store.RecordEvent(context.Background(), eventmodel.Event{
		EventID:        "evt-db",
		TraceID:        "trace-db",
		Source:         "onebot",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC),
		IdempotencyKey: "onebot:db",
	}); err == nil {
		t.Fatal("expected closed sqlite store to return error")
	}
}

func TestFaultInjectionDuplicateEventDetectedByIdempotencyStore(t *testing.T) {
	t.Parallel()

	store, err := runtimecore.OpenSQLiteStateStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer func() { _ = store.Close() }()

	key := "onebot:dup:1"
	if err := store.SaveIdempotencyKey(context.Background(), key, "evt-dup-1"); err != nil {
		t.Fatalf("save idempotency key: %v", err)
	}
	found, err := store.HasIdempotencyKey(context.Background(), key)
	if err != nil {
		t.Fatalf("has idempotency key: %v", err)
	}
	if !found {
		t.Fatal("expected duplicate key to be detectable")
	}
}

func TestFaultInjectionOutOfOrderScheduleStillProducesValidEvent(t *testing.T) {
	t.Parallel()

	scheduler := runtimecore.NewScheduler()
	if err := scheduler.Register(runtimecore.SchedulePlan{
		ID:        "oneshot-old",
		Kind:      runtimecore.ScheduleKindOneShot,
		ExecuteAt: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Source:    "scheduler",
		EventType: "schedule.triggered",
	}); err != nil {
		t.Fatalf("register one-shot plan: %v", err)
	}
	event, err := scheduler.Trigger("oneshot-old")
	if err != nil {
		t.Fatalf("trigger out-of-order plan: %v", err)
	}
	if event.Type != "schedule.triggered" {
		t.Fatalf("unexpected event %+v", event)
	}
}
