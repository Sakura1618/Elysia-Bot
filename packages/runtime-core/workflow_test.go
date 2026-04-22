package runtimecore

import (
	"context"
	"strings"
	"testing"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
)

func testWorkflowObservabilityContext(pluginID string, suffix string) WorkflowObservabilityContext {
	ctx := eventmodel.ExecutionContext{
		TraceID:       `trace-` + suffix,
		EventID:       `evt-` + suffix,
		PluginID:      pluginID,
		RunID:         `run-` + suffix,
		CorrelationID: `corr-` + suffix,
	}
	return WorkflowObservabilityContextFromExecutionContext(ctx)
}

func testWorkflowContext(ctx context.Context, pluginID string, suffix string) context.Context {
	return WithWorkflowObservabilityContext(ctx, testWorkflowObservabilityContext(pluginID, suffix))
}

func TestWorkflowAdvanceResumeAndPersistState(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 3, 15, 0, 0, 0, time.UTC)
	workflow := NewWorkflow(
		"wf-1",
		WorkflowStep{Kind: WorkflowStepKindStep, Name: "step-1"},
		WorkflowStep{Kind: WorkflowStepKindPersist, Name: "state-key", Value: "state-value"},
		WorkflowStep{Kind: WorkflowStepKindCallJob, Name: "job-key", Value: "job-ai"},
		WorkflowStep{Kind: WorkflowStepKindWaitEvent, Name: "wait-user", Value: "message.received"},
		WorkflowStep{Kind: WorkflowStepKindSleep, Name: "sleep-1", Value: "1s"},
		WorkflowStep{Kind: WorkflowStepKindCompensate, Name: "compensate-1"},
	)

	var err error
	workflow, err = workflow.Advance(now)
	if err != nil {
		t.Fatalf("advance step: %v", err)
	}
	workflow, err = workflow.Advance(now)
	if err != nil {
		t.Fatalf("persist step: %v", err)
	}
	workflow, err = workflow.Advance(now)
	if err != nil {
		t.Fatalf("call job step: %v", err)
	}
	if workflow.State["state-key"] != "state-value" || workflow.State["job-key"] != "job-ai" {
		t.Fatalf("unexpected workflow state %+v", workflow.State)
	}

	workflow, err = workflow.Advance(now)
	if err != nil {
		t.Fatalf("wait event step: %v", err)
	}
	if workflow.WaitingFor != "message.received" {
		t.Fatalf("expected waiting state, got %+v", workflow)
	}

	workflow, err = workflow.ResumeWithEvent("message.received")
	if err != nil {
		t.Fatalf("resume with event: %v", err)
	}
	workflow, err = workflow.Advance(now)
	if err != nil {
		t.Fatalf("sleep step: %v", err)
	}
	workflow, err = workflow.ResumeAfterSleep(now.Add(2 * time.Second))
	if err != nil {
		t.Fatalf("resume after sleep: %v", err)
	}
	workflow, err = workflow.Advance(now)
	if err != nil {
		t.Fatalf("compensate step: %v", err)
	}
	if !workflow.Completed || !workflow.Compensated {
		t.Fatalf("expected completed compensated workflow, got %+v", workflow)
	}
}

func TestWorkflowRejectsWrongResumeEvent(t *testing.T) {
	t.Parallel()

	workflow := NewWorkflow("wf-2", WorkflowStep{Kind: WorkflowStepKindWaitEvent, Name: "wait", Value: "message.received"})
	var err error
	workflow, err = workflow.Advance(time.Now().UTC())
	if err != nil {
		t.Fatalf("advance wait step: %v", err)
	}
	if _, err := workflow.ResumeWithEvent("schedule.triggered"); err == nil {
		t.Fatal("expected wrong event type to fail resume")
	}
}

func TestWorkflowRuntimeRestoresPersistedSleepingWorkflow(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	now := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	sleepingUntil := now.Add(-time.Second)
	workflow := NewWorkflow(
		`wf-runtime-restore`,
		WorkflowStep{Kind: WorkflowStepKindSleep, Name: `sleep`, Value: `1s`},
		WorkflowStep{Kind: WorkflowStepKindCompensate, Name: `complete`},
	)
	workflow.CurrentIndex = 0
	workflow.SleepingUntil = &sleepingUntil
	createdAt := now.Add(-2 * time.Minute)

	if err := store.SaveWorkflowInstance(context.Background(), WorkflowInstanceState{
		WorkflowID: `wf-runtime-restore`,
		PluginID:   `plugin-workflow-demo`,
		Status:     WorkflowRuntimeStatusSleeping,
		Workflow:   workflow,
		CreatedAt:  createdAt,
		UpdatedAt:  createdAt,
	}); err != nil {
		t.Fatalf(`save workflow instance: %v`, err)
	}

	runtime := NewWorkflowRuntime(store)
	runtime.now = func() time.Time { return now }
	if err := runtime.Restore(context.Background()); err != nil {
		t.Fatalf(`restore workflow runtime: %v`, err)
	}
	loaded, err := runtime.Load(context.Background(), `wf-runtime-restore`)
	if err != nil {
		t.Fatalf(`load restored workflow: %v`, err)
	}
	if loaded.Status != WorkflowRuntimeStatusCompleted || !loaded.Workflow.Completed || !loaded.Workflow.Compensated {
		t.Fatalf(`expected sleeping workflow to finish during restore, got %+v`, loaded)
	}
	snapshot := runtime.LastRecoverySnapshot()
	if snapshot.TotalWorkflows != 1 || snapshot.RecoveredWorkflows != 1 || snapshot.StatusCounts[WorkflowRuntimeStatusCompleted] != 1 {
		t.Fatalf(`expected workflow recovery snapshot to describe restored workflow, got %+v`, snapshot)
	}
	persisted, err := store.LoadWorkflowInstance(context.Background(), `wf-runtime-restore`)
	if err != nil {
		t.Fatalf(`load persisted restored workflow: %v`, err)
	}
	if persisted.Status != WorkflowRuntimeStatusCompleted || !persisted.Workflow.Completed {
		t.Fatalf(`expected restored workflow to persist completed state, got %+v`, persisted)
	}
}

func TestWorkflowRuntimeStartOrResumeRejectsOwnerMismatch(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	runtime := NewWorkflowRuntime(store)
	runtime.now = func() time.Time { return time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC) }
	ctx := context.Background()
	initial := NewWorkflow(
		`wf-owner-mismatch`,
		WorkflowStep{Kind: WorkflowStepKindPersist, Name: `greeting`, Value: `hello`},
		WorkflowStep{Kind: WorkflowStepKindWaitEvent, Name: `wait`, Value: `message.received`},
		WorkflowStep{Kind: WorkflowStepKindCompensate, Name: `complete`},
	)

	started, err := runtime.StartOrResume(testWorkflowContext(ctx, `plugin-workflow-demo`, `start-owner-mismatch`), `wf-owner-mismatch`, `plugin-workflow-demo`, `message.received`, `evt-start-owner-mismatch`, initial)
	if err != nil {
		t.Fatalf(`start workflow: %v`, err)
	}
	if !started.Started || started.Instance.PluginID != `plugin-workflow-demo` || started.Instance.Status != WorkflowRuntimeStatusWaitingEvent {
		t.Fatalf(`expected started workflow to persist original owner, got %+v`, started)
	}

	_, err = runtime.StartOrResume(testWorkflowContext(ctx, `plugin-admin`, `collision-owner-mismatch`), `wf-owner-mismatch`, `plugin-admin`, `message.received`, `evt-collision-owner-mismatch`, Workflow{})
	if err == nil {
		t.Fatal(`expected owner mismatch to reject resume/start collision`)
	}
	if !strings.Contains(err.Error(), `workflow "wf-owner-mismatch" is owned by plugin "plugin-workflow-demo", not "plugin-admin"`) {
		t.Fatalf(`expected owner mismatch error, got %v`, err)
	}

	persisted, err := store.LoadWorkflowInstance(ctx, `wf-owner-mismatch`)
	if err != nil {
		t.Fatalf(`load workflow after owner mismatch: %v`, err)
	}
	if persisted.PluginID != `plugin-workflow-demo` || persisted.Status != WorkflowRuntimeStatusWaitingEvent {
		t.Fatalf(`expected workflow owner/status to stay unchanged after mismatch, got %+v`, persisted)
	}
	if persisted.LastEventID != `evt-start-owner-mismatch` || persisted.LastEventType != `message.received` {
		t.Fatalf(`expected mismatch to leave persisted event cursor unchanged, got %+v`, persisted)
	}
	if persisted.TraceID != `trace-start-owner-mismatch` || persisted.EventID != `evt-start-owner-mismatch` || persisted.RunID != `run-start-owner-mismatch` || persisted.CorrelationID != `corr-start-owner-mismatch` {
		t.Fatalf(`expected workflow origin observability ids to remain stable after mismatch, got %+v`, persisted)
	}
}

func TestWorkflowRuntimeStartOrResumeKeepsOwnerOnMatchingResume(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	runtime := NewWorkflowRuntime(store)
	now := time.Date(2026, 4, 22, 10, 30, 0, 0, time.UTC)
	runtime.now = func() time.Time { return now }
	ctx := context.Background()
	initial := NewWorkflow(
		`wf-owner-stable`,
		WorkflowStep{Kind: WorkflowStepKindWaitEvent, Name: `wait`, Value: `message.received`},
		WorkflowStep{Kind: WorkflowStepKindCompensate, Name: `complete`},
	)

	if _, err := runtime.StartOrResume(testWorkflowContext(ctx, `plugin-workflow-demo`, `start-owner-stable`), `wf-owner-stable`, `plugin-workflow-demo`, `message.received`, `evt-start-owner-stable`, initial); err != nil {
		t.Fatalf(`start workflow: %v`, err)
	}
	resumed, err := runtime.StartOrResume(testWorkflowContext(ctx, `plugin-workflow-demo`, `resume-owner-stable`), `wf-owner-stable`, `plugin-workflow-demo`, `message.received`, `evt-resume-owner-stable`, Workflow{})
	if err != nil {
		t.Fatalf(`resume workflow: %v`, err)
	}
	if !resumed.Resumed || resumed.Instance.PluginID != `plugin-workflow-demo` || resumed.Instance.Status != WorkflowRuntimeStatusCompleted {
		t.Fatalf(`expected matching resume to keep owner and complete workflow, got %+v`, resumed)
	}

	persisted, err := store.LoadWorkflowInstance(ctx, `wf-owner-stable`)
	if err != nil {
		t.Fatalf(`load resumed workflow: %v`, err)
	}
	if persisted.PluginID != `plugin-workflow-demo` || persisted.Status != WorkflowRuntimeStatusCompleted || !persisted.Workflow.Completed {
		t.Fatalf(`expected persisted workflow owner to remain stable after resume, got %+v`, persisted)
	}
	if persisted.LastEventID != `evt-resume-owner-stable` || persisted.LastEventType != `message.received` {
		t.Fatalf(`expected persisted workflow to record resume event, got %+v`, persisted)
	}
	if persisted.TraceID != `trace-start-owner-stable` || persisted.EventID != `evt-start-owner-stable` || persisted.RunID != `run-start-owner-stable` || persisted.CorrelationID != `corr-start-owner-stable` {
		t.Fatalf(`expected persisted workflow origin observability ids to stay stable on resume, got %+v`, persisted)
	}
}

func TestWorkflowRuntimeStartOrResumeRecordsWave2CMetrics(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	metrics := NewMetricsRegistry()
	runtime := NewWorkflowRuntime(store)
	runtime.SetMetrics(metrics)
	now := time.Date(2026, 4, 22, 11, 0, 0, 0, time.UTC)
	runtime.now = func() time.Time { return now }
	ctx := context.Background()
	initial := NewWorkflow(
		`wf-metrics`,
		WorkflowStep{Kind: WorkflowStepKindWaitEvent, Name: `wait`, Value: `message.received`},
		WorkflowStep{Kind: WorkflowStepKindCompensate, Name: `complete`},
	)

	started, err := runtime.StartOrResume(testWorkflowContext(ctx, `plugin-workflow-demo`, `start-metrics`), `wf-metrics`, `plugin-workflow-demo`, `message.received`, `evt-start-metrics`, initial)
	if err != nil {
		t.Fatalf(`start workflow: %v`, err)
	}
	if !started.Started || started.Instance.Status != WorkflowRuntimeStatusWaitingEvent {
		t.Fatalf(`expected waiting workflow after start, got %+v`, started)
	}
	resumed, err := runtime.StartOrResume(testWorkflowContext(ctx, `plugin-workflow-demo`, `resume-metrics`), `wf-metrics`, `plugin-workflow-demo`, `message.received`, `evt-resume-metrics`, Workflow{})
	if err != nil {
		t.Fatalf(`resume workflow: %v`, err)
	}
	if !resumed.Resumed || resumed.Instance.Status != WorkflowRuntimeStatusCompleted {
		t.Fatalf(`expected completed workflow after resume, got %+v`, resumed)
	}

	output := metrics.RenderPrometheus()
	for _, expected := range []string{
		`bot_platform_workflow_transition_total{plugin_id="plugin-workflow-demo",outcome="started"} 1`,
		`bot_platform_workflow_transition_total{plugin_id="plugin-workflow-demo",outcome="resumed"} 1`,
		`bot_platform_workflow_instance_count{status="completed"} 1`,
		`bot_platform_workflow_instance_count{status="waiting_event"} 0`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf(`expected workflow metrics output to contain %q, got %s`, expected, output)
		}
	}
	for _, forbidden := range []string{`trace_id=`, `event_id=`, `run_id=`, `correlation_id=`} {
		if strings.Contains(output, forbidden) {
			t.Fatalf(`expected workflow metrics labels to remain bounded without %q, got %s`, forbidden, output)
		}
	}
}

func TestWorkflowRuntimeStartOrResumePersistsOriginObservabilityIDs(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	runtime := NewWorkflowRuntime(store)
	runtime.now = func() time.Time { return time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC) }
	ctx := context.Background()
	observability := testWorkflowObservabilityContext(`plugin-workflow-demo`, `persist-observability`)
	initial := NewWorkflow(
		`wf-observability`,
		WorkflowStep{Kind: WorkflowStepKindWaitEvent, Name: `wait`, Value: `message.received`},
		WorkflowStep{Kind: WorkflowStepKindCompensate, Name: `complete`},
	)

	transition, err := runtime.StartOrResume(WithWorkflowObservabilityContext(ctx, observability), `wf-observability`, `plugin-workflow-demo`, `message.received`, observability.EventID, initial)
	if err != nil {
		t.Fatalf(`start workflow: %v`, err)
	}
	if !transition.Started {
		t.Fatalf(`expected workflow start transition, got %+v`, transition)
	}
	if transition.Instance.TraceID != observability.TraceID || transition.Instance.EventID != observability.EventID || transition.Instance.PluginID != observability.PluginID || transition.Instance.RunID != observability.RunID || transition.Instance.CorrelationID != observability.CorrelationID {
		t.Fatalf(`expected transition instance to keep origin observability ids, got %+v`, transition.Instance)
	}
	persisted, err := store.LoadWorkflowInstance(ctx, `wf-observability`)
	if err != nil {
		t.Fatalf(`load workflow instance: %v`, err)
	}
	if persisted.TraceID != observability.TraceID || persisted.EventID != observability.EventID || persisted.PluginID != observability.PluginID || persisted.RunID != observability.RunID || persisted.CorrelationID != observability.CorrelationID {
		t.Fatalf(`expected persisted workflow observability ids, got %+v`, persisted)
	}
	if persisted.LastEventID != observability.EventID || persisted.LastEventType != `message.received` {
		t.Fatalf(`expected last event cursor to still point at triggering event, got %+v`, persisted)
	}
}
