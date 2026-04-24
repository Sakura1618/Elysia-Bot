package pluginaichat

import (
	"context"
	"errors"
	"testing"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type providerStub struct {
	response string
	err      error
}

func (p providerStub) Generate(ctx context.Context, prompt string) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	return p.response, nil
}

type sessionRecorder struct {
	sessions []pluginsdk.SessionState
}

func (s *sessionRecorder) SaveSession(ctx context.Context, session pluginsdk.SessionState) error {
	s.sessions = append(s.sessions, session)
	return nil
}

type jobQueueStub struct {
	jobs map[string]pluginsdk.Job
	now  func() time.Time
}

func newJobQueueStub() *jobQueueStub {
	return &jobQueueStub{
		jobs: map[string]pluginsdk.Job{},
		now:  time.Now().UTC,
	}
}

func (q *jobQueueStub) Enqueue(_ context.Context, job pluginsdk.Job) error {
	if job.Status == "" {
		job.Status = pluginsdk.JobStatusPending
	}
	if err := job.Validate(); err != nil {
		return err
	}
	if _, exists := q.jobs[job.ID]; exists {
		return errors.New("job already exists")
	}
	q.jobs[job.ID] = job
	return nil
}

func (q *jobQueueStub) MarkRunning(_ context.Context, id string) (pluginsdk.Job, error) {
	return q.transition(id, pluginsdk.JobStatusRunning, "")
}

func (q *jobQueueStub) Complete(_ context.Context, id string) (pluginsdk.Job, error) {
	return q.transition(id, pluginsdk.JobStatusDone, "")
}

func (q *jobQueueStub) Fail(_ context.Context, id string, reason string) (pluginsdk.Job, error) {
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

func (q *jobQueueStub) Inspect(_ context.Context, id string) (pluginsdk.Job, error) {
	job, exists := q.jobs[id]
	if !exists {
		return pluginsdk.Job{}, errors.New("job not found")
	}
	return job, nil
}

func (q *jobQueueStub) transition(id string, next pluginsdk.JobStatus, reason string) (pluginsdk.Job, error) {
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

func (q *jobQueueStub) List() []pluginsdk.Job {
	items := make([]pluginsdk.Job, 0, len(q.jobs))
	for _, job := range q.jobs {
		items = append(items, job)
	}
	return items
}

type replyRecorder struct {
	text   string
	err    error
	handle eventmodel.ReplyHandle
}

func (r *replyRecorder) ReplyText(handle eventmodel.ReplyHandle, text string) error {
	if r.err != nil {
		return r.err
	}
	r.handle = handle
	r.text = text
	return nil
}
func (r *replyRecorder) ReplyImage(handle eventmodel.ReplyHandle, imageURL string) error { return nil }
func (r *replyRecorder) ReplyFile(handle eventmodel.ReplyHandle, fileURL string) error   { return nil }

func TestPluginAIChatEnqueuesJobAndPersistsSession(t *testing.T) {
	t.Parallel()

	queue := newJobQueueStub()
	sessions := &sessionRecorder{}
	plugin := New(queue, providerStub{response: "hello"}, sessions, &replyRecorder{})

	err := plugin.OnEvent(eventmodel.Event{
		EventID:        "evt-ai-1",
		TraceID:        "trace-ai-1",
		Source:         "webhook",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 3, 14, 0, 0, 0, time.UTC),
		IdempotencyKey: "webhook:ai:1",
		Actor:          &eventmodel.Actor{ID: "user-1", Type: "user"},
		Message:        &eventmodel.Message{Text: "hello ai"},
	}, eventmodel.ExecutionContext{TraceID: "trace-ai-1", EventID: "evt-ai-1", Reply: &eventmodel.ReplyHandle{TargetID: "group-42", Capability: "onebot.reply"}})
	if err != nil {
		t.Fatalf("on event: %v", err)
	}
	if len(queue.List()) != 1 {
		t.Fatalf("expected one queued job, got %+v", queue.List())
	}
	queued := queue.List()[0]
	if queued.Correlation != "webhook:ai:1" {
		t.Fatalf("expected queued job correlation to be stamped, got %+v", queued)
	}
	dispatch, _ := queued.Payload["dispatch"].(map[string]any)
	if got, _ := dispatch["target_plugin_id"].(string); got != plugin.Manifest.ID {
		t.Fatalf("expected queued job to target plugin %q, got %+v", plugin.Manifest.ID, queued.Payload)
	}
	if got, _ := dispatch["permission"].(string); got != "job:run" {
		t.Fatalf("expected queued job to declare job:run permission, got %+v", queued.Payload)
	}
	if queued.TraceID != "trace-ai-1" || queued.EventID != "evt-ai-1" || queued.RunID != "" {
		t.Fatalf("expected trace/event propagation on job contract, got %+v", queued)
	}
	if len(sessions.sessions) != 1 || sessions.sessions[0].SessionID != "session-user-1" {
		t.Fatalf("unexpected sessions %+v", sessions.sessions)
	}
}

func TestPluginAIChatProcessesJobAndReplies(t *testing.T) {
	t.Parallel()

	queue := newJobQueueStub()
	sessions := &sessionRecorder{}
	replies := &replyRecorder{}
	plugin := New(queue, providerStub{response: "ai response"}, sessions, replies)

	job := pluginsdk.NewJob("job-ai-chat-evt-2", "ai.chat", 1, 30*time.Second)
	job.Payload = map[string]any{"prompt": "hello ai"}
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	err := plugin.ProcessJob(context.Background(), job.ID, eventmodel.ReplyHandle{Capability: "onebot.reply", TargetID: "group-42"})
	if err != nil {
		t.Fatalf("process job: %v", err)
	}
	if replies.text != "ai response" {
		t.Fatalf("unexpected reply text %q", replies.text)
	}
	stored, err := queue.Inspect(context.Background(), job.ID)
	if err != nil || stored.Status != pluginsdk.JobStatusDone {
		t.Fatalf("expected done job, got %+v err=%v", stored, err)
	}
}

func TestPluginAIChatProcessJobRespectsQueueOwnedRunningState(t *testing.T) {
	t.Parallel()

	queue := newJobQueueStub()
	replies := &replyRecorder{}
	plugin := New(queue, providerStub{response: "ai response"}, &sessionRecorder{}, replies)

	job := pluginsdk.NewJob("job-ai-chat-running-owned", "ai.chat", 1, 30*time.Second)
	job.Payload = map[string]any{"prompt": "hello ai"}
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if _, err := queue.MarkRunning(context.Background(), job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	if err := plugin.ProcessJob(context.Background(), job.ID, eventmodel.ReplyHandle{Capability: "onebot.reply", TargetID: "group-42"}); err != nil {
		t.Fatalf("process already-running job: %v", err)
	}
	stored, err := queue.Inspect(context.Background(), job.ID)
	if err != nil || stored.Status != pluginsdk.JobStatusDone {
		t.Fatalf("expected done job after queue-owned running path, got %+v err=%v", stored, err)
	}
}

func TestPluginAIChatFailureFeedbackAfterRetryExhaustion(t *testing.T) {
	t.Parallel()

	queue := newJobQueueStub()
	replies := &replyRecorder{}
	plugin := New(queue, providerStub{err: errors.New("upstream timeout")}, &sessionRecorder{}, replies)

	job := pluginsdk.NewJob("job-ai-chat-evt-3", "ai.chat", 0, 30*time.Second)
	job.Payload = map[string]any{"prompt": "hello ai"}
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	err := plugin.ProcessJob(context.Background(), job.ID, eventmodel.ReplyHandle{Capability: "onebot.reply", TargetID: "group-42"})
	if err != nil {
		t.Fatalf("expected failure feedback path to return nil after reply, got %v", err)
	}
	if replies.text != "AI request failed: upstream timeout" {
		t.Fatalf("unexpected failure feedback %q", replies.text)
	}
	stored, err := queue.Inspect(context.Background(), job.ID)
	if err != nil || stored.Status != pluginsdk.JobStatusDead {
		t.Fatalf("expected dead job, got %+v err=%v", stored, err)
	}
}

func TestPluginAIChatRejectsEventWithoutReplyHandle(t *testing.T) {
	t.Parallel()

	queue := newJobQueueStub()
	plugin := New(queue, providerStub{response: "hello"}, &sessionRecorder{}, &replyRecorder{})

	err := plugin.OnEvent(eventmodel.Event{
		EventID:        "evt-ai-no-reply",
		TraceID:        "trace-ai-no-reply",
		Source:         "runtime-ai",
		Type:           "message.received",
		Timestamp:      time.Date(2026, 4, 9, 10, 0, 0, 0, time.UTC),
		IdempotencyKey: "runtime-ai:user:hello",
		Actor:          &eventmodel.Actor{ID: "user-1", Type: "user"},
		Message:        &eventmodel.Message{Text: "hello ai"},
	}, eventmodel.ExecutionContext{TraceID: "trace-ai-no-reply", EventID: "evt-ai-no-reply"})
	if err == nil || err.Error() != "reply handle is required" {
		t.Fatalf("expected missing reply handle error, got %v", err)
	}
	if len(queue.List()) != 0 {
		t.Fatalf("expected no queued job after invalid event context, got %+v", queue.List())
	}
}

func TestPluginAIChatManifestAdoptsV1Contract(t *testing.T) {
	t.Parallel()

	plugin := New(newJobQueueStub(), providerStub{response: "hello"}, &sessionRecorder{}, &replyRecorder{})
	manifest := plugin.Manifest

	if manifest.SchemaVersion != pluginsdk.SupportedPluginManifestSchemaVersion {
		t.Fatalf("manifest schemaVersion = %q, want %q", manifest.SchemaVersion, pluginsdk.SupportedPluginManifestSchemaVersion)
	}
	if manifest.Publish == nil {
		t.Fatal("manifest publish metadata is required")
	}
	if manifest.Publish.SourceType != pluginsdk.PublishSourceTypeGit {
		t.Fatalf("manifest publish sourceType = %q, want %q", manifest.Publish.SourceType, pluginsdk.PublishSourceTypeGit)
	}
	if manifest.Publish.SourceURI != pluginAIChatPublishSourceURI {
		t.Fatalf("manifest publish sourceUri = %q, want %q", manifest.Publish.SourceURI, pluginAIChatPublishSourceURI)
	}
	if manifest.Publish.RuntimeVersionRange != pluginAIChatRuntimeVersionRange {
		t.Fatalf("manifest publish runtimeVersionRange = %q, want %q", manifest.Publish.RuntimeVersionRange, pluginAIChatRuntimeVersionRange)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("expected plugin-ai-chat manifest to validate, got %v", err)
	}
}

func TestPluginAIChatReplyFailureTransitionsJobOutOfRunning(t *testing.T) {
	t.Parallel()

	queue := newJobQueueStub()
	replies := &replyRecorder{err: errors.New("reply send failed")}
	plugin := New(queue, providerStub{response: "ai response"}, &sessionRecorder{}, replies)

	job := pluginsdk.NewJob("job-ai-chat-evt-reply-fail", "ai.chat", 1, 30*time.Second)
	job.Payload = map[string]any{"prompt": "hello ai"}
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	err := plugin.ProcessJob(context.Background(), job.ID, eventmodel.ReplyHandle{Capability: "onebot.reply", TargetID: "group-42"})
	if err == nil || err.Error() != "reply send failed" {
		t.Fatalf("expected reply send failure, got %v", err)
	}
	stored, inspectErr := queue.Inspect(context.Background(), job.ID)
	if inspectErr != nil {
		t.Fatalf("inspect job: %v", inspectErr)
	}
	if stored.Status != pluginsdk.JobStatusRetrying {
		t.Fatalf("expected reply failure to move job into retrying, got %+v", stored)
	}
}

func TestPluginAIChatDefinitionExposesJobHandler(t *testing.T) {
	t.Parallel()

	plugin := New(newJobQueueStub(), providerStub{response: "hello"}, &sessionRecorder{}, &replyRecorder{})
	definition := plugin.Definition()
	if definition.Handlers.Job == nil {
		t.Fatal("expected plugin definition to expose a Job handler")
	}
	if len(definition.Manifest.Permissions) == 0 {
		t.Fatal("expected manifest permissions to be present")
	}
	found := false
	for _, permission := range definition.Manifest.Permissions {
		if permission == "job:run" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected job:run permission in manifest, got %+v", definition.Manifest.Permissions)
	}
}

func TestPluginAIChatOnJobUsesExecutionContextReply(t *testing.T) {
	t.Parallel()

	queue := newJobQueueStub()
	replies := &replyRecorder{}
	plugin := New(queue, providerStub{response: "ai via runtime"}, &sessionRecorder{}, replies)

	job := pluginsdk.NewJob("job-ai-chat-evt-4", "ai.chat", 1, 30*time.Second)
	job.Payload = map[string]any{"prompt": "hello ai"}
	if err := queue.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	err := plugin.OnJob(pluginsdk.JobInvocation{ID: job.ID, Type: job.Type}, eventmodel.ExecutionContext{
		TraceID:       "trace-ai-job-1",
		EventID:       "evt-ai-job-1",
		RunID:         "run-ai-job-1",
		CorrelationID: "corr-ai-job-1",
		Reply:         &eventmodel.ReplyHandle{Capability: "onebot.reply", TargetID: "group-42"},
	})
	if err != nil {
		t.Fatalf("on job: %v", err)
	}
	if replies.text != "ai via runtime" {
		t.Fatalf("unexpected reply text %q", replies.text)
	}
	for key, expected := range map[string]string{
		"trace_id":       "trace-ai-job-1",
		"event_id":       "evt-ai-job-1",
		"run_id":         "run-ai-job-1",
		"correlation_id": "corr-ai-job-1",
		"plugin_id":      "plugin-ai-chat",
	} {
		if got, _ := replies.handle.Metadata[key].(string); got != expected {
			t.Fatalf("expected reply metadata %s=%q, got %+v", key, expected, replies.handle.Metadata)
		}
	}
	stored, err := queue.Inspect(context.Background(), job.ID)
	if err != nil || stored.Status != pluginsdk.JobStatusDone {
		t.Fatalf("expected done job after OnJob, got %+v err=%v", stored, err)
	}
}
