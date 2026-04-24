package pluginworkflowdemo

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
)

type replyRecorder struct {
	handle eventmodel.ReplyHandle
	texts  []string
}

type workflowDemoQueueStub struct {
	jobs map[string]pluginsdk.Job
}

func newWorkflowDemoQueueStub() *workflowDemoQueueStub {
	return &workflowDemoQueueStub{jobs: map[string]pluginsdk.Job{}}
}

func (q *workflowDemoQueueStub) Enqueue(_ context.Context, job pluginsdk.Job) error {
	if q.jobs == nil {
		q.jobs = map[string]pluginsdk.Job{}
	}
	q.jobs[job.ID] = job
	return nil
}

func (q *workflowDemoQueueStub) Inspect(_ context.Context, id string) (pluginsdk.Job, error) {
	job, ok := q.jobs[id]
	if !ok {
		return pluginsdk.Job{}, errors.New("job not found")
	}
	return job, nil
}

func (q *workflowDemoQueueStub) Cancel(_ context.Context, id string) (pluginsdk.Job, error) {
	job, ok := q.jobs[id]
	if !ok {
		return pluginsdk.Job{}, errors.New("job not found")
	}
	job.Status = pluginsdk.JobStatusCancelled
	q.jobs[id] = job
	return job, nil
}

func (r *replyRecorder) ReplyText(handle eventmodel.ReplyHandle, text string) error {
	r.handle = handle
	r.texts = append(r.texts, text)
	return nil
}

func (r *replyRecorder) ReplyImage(handle eventmodel.ReplyHandle, imageURL string) error { return nil }
func (r *replyRecorder) ReplyFile(handle eventmodel.ReplyHandle, fileURL string) error   { return nil }

func TestWorkflowDemoStartsAndResumesWorkflowFromRuntimeOwnedStore(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := runtimecore.OpenSQLiteStateStore(filepath.Join(t.TempDir(), `state.db`))
	if err != nil {
		t.Fatalf(`open sqlite store: %v`, err)
	}
	defer func() { _ = store.Close() }()
	runtime := runtimecore.NewWorkflowRuntime(store)
	queue := newWorkflowDemoQueueStub()

	firstReplies := &replyRecorder{}
	firstPlugin := New(firstReplies, queue, runtime)
	first := eventmodel.Event{
		EventID:        `evt-1`,
		TraceID:        `trace-1`,
		Source:         `runtime-workflow-demo`,
		Type:           `message.received`,
		IdempotencyKey: `msg:1`,
		Actor:          &eventmodel.Actor{ID: `user-1`, Type: `user`},
		Message:        &eventmodel.Message{Text: `start workflow`},
	}
	ctx1 := eventmodel.ExecutionContext{TraceID: `trace-1`, EventID: `evt-1`, RunID: `run-1`, CorrelationID: `corr-1`, Reply: &eventmodel.ReplyHandle{Capability: `onebot.reply`, TargetID: `group-42`}}
	if err := firstPlugin.OnEvent(first, ctx1); err != nil {
		t.Fatalf(`start workflow: %v`, err)
	}
	if len(firstReplies.texts) != 1 || firstReplies.texts[0] != `workflow started, child job queued; wait for its result, then send another message to continue` {
		t.Fatalf(`unexpected first replies %+v`, firstReplies.texts)
	}
	queuedJob, ok := queue.jobs[workflowChildJobID(`workflow-user-1`)]
	if !ok {
		t.Fatalf(`expected child job to be queued, got %+v`, queue.jobs)
	}
	workflowPayload, _ := queuedJob.Payload[`workflow`].(map[string]any)
	if workflowPayload[`workflow_id`] != `workflow-user-1` || workflowPayload[`plugin_id`] != `plugin-workflow-demo` {
		t.Fatalf(`expected queued child job workflow ownership metadata, got %+v`, queuedJob.Payload)
	}
	for key, expected := range map[string]string{
		`trace_id`:       `trace-1`,
		`event_id`:       `evt-1`,
		`plugin_id`:      `plugin-workflow-demo`,
		`run_id`:         `run-1`,
		`correlation_id`: `corr-1`,
	} {
		if got, _ := firstReplies.handle.Metadata[key].(string); got != expected {
			t.Fatalf(`expected first workflow reply metadata %s=%q, got %+v`, key, expected, firstReplies.handle.Metadata)
		}
	}
	stored, err := store.LoadWorkflowInstance(ctx, `workflow-user-1`)
	if err != nil {
		t.Fatalf(`load started workflow: %v`, err)
	}
	if stored.PluginID != `plugin-workflow-demo` || stored.Status != runtimecore.WorkflowRuntimeStatusWaitingJob {
		t.Fatalf(`expected waiting runtime-owned workflow state, got %+v`, stored)
	}
	if stored.Workflow.WaitingForJob == nil || stored.Workflow.WaitingForJob.JobID != queuedJob.ID || stored.Workflow.State[`greeting`] != `start workflow` || stored.Workflow.Completed {
		t.Fatalf(`expected persisted workflow waiting child-job state, got %+v`, stored.Workflow)
	}
	if stored.TraceID != `trace-1` || stored.EventID != `evt-1` || stored.RunID != `run-1` || stored.CorrelationID != `corr-1` {
		t.Fatalf(`expected persisted workflow observability ids from first event, got %+v`, stored)
	}

	restartedRuntime := runtimecore.NewWorkflowRuntime(store)
	if err := restartedRuntime.Restore(ctx); err != nil {
		t.Fatalf(`restore workflow runtime: %v`, err)
	}
	snapshot := restartedRuntime.LastRecoverySnapshot()
	if snapshot.TotalWorkflows != 1 || snapshot.RecoveredWorkflows != 1 || snapshot.StatusCounts[runtimecore.WorkflowRuntimeStatusWaitingJob] != 1 {
		t.Fatalf(`expected workflow recovery snapshot after restart, got %+v`, snapshot)
	}
	if _, err := restartedRuntime.ResumeFromChildJob(ctx, `workflow-user-1`, `plugin-workflow-demo`, runtimecore.WorkflowChildJobResume{JobID: queuedJob.ID, Status: runtimecore.JobStatusDead, ReasonCode: runtimecore.JobReasonCodeExecutionDead, LastError: `boom`}); err != nil {
		t.Fatalf(`resume workflow from child job: %v`, err)
	}
	secondReplies := &replyRecorder{}
	secondPlugin := New(secondReplies, queue, restartedRuntime)
	second := eventmodel.Event{
		EventID:        `evt-2`,
		TraceID:        `trace-2`,
		Source:         `runtime-workflow-demo`,
		Type:           `message.received`,
		IdempotencyKey: `msg:2`,
		Actor:          &eventmodel.Actor{ID: `user-1`, Type: `user`},
		Message:        &eventmodel.Message{Text: `continue`},
	}
	ctx2 := eventmodel.ExecutionContext{TraceID: `trace-2`, EventID: `evt-2`, RunID: `run-2`, CorrelationID: `corr-2`, Reply: &eventmodel.ReplyHandle{Capability: `onebot.reply`, TargetID: `group-42`}}
	if err := secondPlugin.OnEvent(second, ctx2); err != nil {
		t.Fatalf(`resume workflow: %v`, err)
	}
	if len(secondReplies.texts) != 1 || secondReplies.texts[0] != `workflow resumed after child job failure and compensation completed` {
		t.Fatalf(`unexpected second replies %+v`, secondReplies.texts)
	}
	for key, expected := range map[string]string{
		`trace_id`:       `trace-1`,
		`event_id`:       `evt-1`,
		`plugin_id`:      `plugin-workflow-demo`,
		`run_id`:         `run-1`,
		`correlation_id`: `corr-1`,
	} {
		if got, _ := secondReplies.handle.Metadata[key].(string); got != expected {
			t.Fatalf(`expected resumed workflow reply metadata %s=%q, got %+v`, key, expected, secondReplies.handle.Metadata)
		}
	}
	completed, err := store.LoadWorkflowInstance(ctx, `workflow-user-1`)
	if err != nil {
		t.Fatalf(`load completed workflow: %v`, err)
	}
	if completed.Status != runtimecore.WorkflowRuntimeStatusCompleted || !completed.Workflow.Completed || !completed.Workflow.Compensated {
		t.Fatalf(`expected completed workflow after resume, got %+v`, completed)
	}
	if completed.LastEventID != `evt-2` || completed.LastEventType != `message.received` {
		t.Fatalf(`expected last event metadata to update on resume, got %+v`, completed)
	}
	if completed.TraceID != `trace-1` || completed.EventID != `evt-1` || completed.RunID != `run-1` || completed.CorrelationID != `corr-1` {
		t.Fatalf(`expected origin observability ids to remain stable on resume, got %+v`, completed)
	}
}

func TestWorkflowDemoManifestAdoptsV1Contract(t *testing.T) {
	t.Parallel()

	plugin := New(&replyRecorder{}, newWorkflowDemoQueueStub(), nil)
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
	if manifest.Publish.SourceURI != pluginWorkflowDemoPublishSourceURI {
		t.Fatalf("manifest publish sourceUri = %q, want %q", manifest.Publish.SourceURI, pluginWorkflowDemoPublishSourceURI)
	}
	if manifest.Publish.RuntimeVersionRange != pluginWorkflowDemoRuntimeVersionRange {
		t.Fatalf("manifest publish runtimeVersionRange = %q, want %q", manifest.Publish.RuntimeVersionRange, pluginWorkflowDemoRuntimeVersionRange)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("expected plugin-workflow-demo manifest to validate, got %v", err)
	}
}

func TestWorkflowDemoStartsFreshRunAfterCompletedWorkflowForSameActor(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := runtimecore.OpenSQLiteStateStore(filepath.Join(t.TempDir(), `repeat.db`))
	if err != nil {
		t.Fatalf(`open sqlite store: %v`, err)
	}
	defer func() { _ = store.Close() }()
	runtime := runtimecore.NewWorkflowRuntime(store)
	queue := newWorkflowDemoQueueStub()
	plugin := New(&replyRecorder{}, queue, runtime)
	plugin.Now = func() time.Time { return time.Date(2026, 4, 25, 11, 0, 0, 0, time.UTC) }
	start := eventmodel.Event{EventID: `evt-repeat-1`, TraceID: `trace-repeat-1`, Source: `runtime-workflow-demo`, Type: `message.received`, IdempotencyKey: `msg:repeat:1`, Actor: &eventmodel.Actor{ID: `repeat-user`, Type: `user`}, Message: &eventmodel.Message{Text: `first run`}}
	ctx1 := eventmodel.ExecutionContext{TraceID: `trace-repeat-1`, EventID: `evt-repeat-1`, RunID: `run-repeat-1`, CorrelationID: `corr-repeat-1`, Reply: &eventmodel.ReplyHandle{Capability: `onebot.reply`, TargetID: `group-42`}}
	if err := plugin.OnEvent(start, ctx1); err != nil {
		t.Fatalf(`start first run: %v`, err)
	}
	if _, err := runtime.ResumeFromChildJob(ctx, `workflow-repeat-user`, `plugin-workflow-demo`, runtimecore.WorkflowChildJobResume{JobID: workflowChildJobID(`workflow-repeat-user`), Status: runtimecore.JobStatusDone}); err != nil {
		t.Fatalf(`resume first run from child job: %v`, err)
	}
	continueEvent := eventmodel.Event{EventID: `evt-repeat-2`, TraceID: `trace-repeat-2`, Source: `runtime-workflow-demo`, Type: `message.received`, IdempotencyKey: `msg:repeat:2`, Actor: &eventmodel.Actor{ID: `repeat-user`, Type: `user`}, Message: &eventmodel.Message{Text: `complete first`}}
	ctx2 := eventmodel.ExecutionContext{TraceID: `trace-repeat-2`, EventID: `evt-repeat-2`, RunID: `run-repeat-2`, CorrelationID: `corr-repeat-2`, Reply: &eventmodel.ReplyHandle{Capability: `onebot.reply`, TargetID: `group-42`}}
	if err := plugin.OnEvent(continueEvent, ctx2); err != nil {
		t.Fatalf(`complete first run: %v`, err)
	}

	plugin.Now = func() time.Time { return time.Date(2026, 4, 25, 11, 5, 0, 0, time.UTC) }
	restartEvent := eventmodel.Event{EventID: `evt-repeat-3`, TraceID: `trace-repeat-3`, Source: `runtime-workflow-demo`, Type: `message.received`, IdempotencyKey: `msg:repeat:3`, Actor: &eventmodel.Actor{ID: `repeat-user`, Type: `user`}, Message: &eventmodel.Message{Text: `second run`}}
	ctx3 := eventmodel.ExecutionContext{TraceID: `trace-repeat-3`, EventID: `evt-repeat-3`, RunID: `run-repeat-3`, CorrelationID: `corr-repeat-3`, Reply: &eventmodel.ReplyHandle{Capability: `onebot.reply`, TargetID: `group-42`}}
	if err := plugin.OnEvent(restartEvent, ctx3); err != nil {
		t.Fatalf(`start second run: %v`, err)
	}
	items, err := store.ListWorkflowInstances(ctx)
	if err != nil {
		t.Fatalf(`list workflow instances: %v`, err)
	}
	if len(items) != 2 {
		t.Fatalf(`expected completed first run plus fresh second run, got %+v`, items)
	}
	freshFound := false
	for _, item := range items {
		if item.WorkflowID == `workflow-repeat-user` {
			continue
		}
		if strings.HasPrefix(item.WorkflowID, `workflow-repeat-user-run-`) && item.Status == runtimecore.WorkflowRuntimeStatusWaitingJob {
			freshFound = true
		}
	}
	if !freshFound {
		t.Fatalf(`expected repeat actor to get a fresh workflow run id, got %+v`, items)
	}
}
