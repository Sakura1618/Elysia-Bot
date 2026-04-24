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
	if workflow.State["state-key"] != "state-value" {
		t.Fatalf("unexpected workflow state %+v", workflow.State)
	}
	if workflow.WaitingForJob == nil || workflow.WaitingForJob.JobID != "job-ai" || workflow.CurrentIndex != 2 {
		t.Fatalf("expected workflow to block on child job, got %+v", workflow)
	}

	workflow, err = workflow.ResumeWithChildJob(WorkflowJobResult{JobID: "job-ai", Status: JobStatusDone})
	if err != nil {
		t.Fatalf("resume with child job: %v", err)
	}
	jobState, ok := workflow.State["job-key"].(map[string]any)
	if !ok || jobState["job_id"] != "job-ai" || jobState["status"] != string(JobStatusDone) {
		t.Fatalf("expected child job result state after resume, got %+v", workflow.State["job-key"])
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
	if !workflow.Completed || workflow.Compensated {
		t.Fatalf("expected completed non-compensated workflow after successful child job, got %+v", workflow)
	}
}

func TestWorkflowCallJobFailureRequiresCompensation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 3, 15, 30, 0, 0, time.UTC)
	workflow := NewWorkflow(
		"wf-call-job-failure",
		WorkflowStep{Kind: WorkflowStepKindCallJob, Name: "job-key", Value: "job-failed"},
		WorkflowStep{Kind: WorkflowStepKindCompensate, Name: "compensate-1"},
	)

	workflow, err := workflow.Advance(now)
	if err != nil {
		t.Fatalf("advance call job step: %v", err)
	}
	if workflow.WaitingForJob == nil || workflow.WaitingForJob.JobID != "job-failed" {
		t.Fatalf("expected waiting child job state, got %+v", workflow)
	}
	workflow, err = workflow.ResumeWithChildJob(WorkflowJobResult{JobID: "job-failed", Status: JobStatusDead, ReasonCode: JobReasonCodeExecutionDead, LastError: "boom"})
	if err != nil {
		t.Fatalf("resume failed child job: %v", err)
	}
	workflow, err = workflow.Advance(now)
	if err != nil {
		t.Fatalf("advance compensate step: %v", err)
	}
	if !workflow.Completed || !workflow.Compensated {
		t.Fatalf("expected compensated workflow after failed child job, got %+v", workflow)
	}
	jobState, ok := workflow.State["job-key"].(map[string]any)
	if !ok || jobState["reason_code"] != string(JobReasonCodeExecutionDead) || jobState["last_error"] != "boom" {
		t.Fatalf("expected failed child job result state, got %+v", workflow.State["job-key"])
	}
	if workflow.LastJobResult == nil || workflow.LastJobResult.Status != JobStatusDead {
		t.Fatalf("expected workflow to retain last child job result, got %+v", workflow.LastJobResult)
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

func TestWorkflowRuntimeResumeFromChildJobPersistsWaitingStateAndSuccessResult(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	runtime := NewWorkflowRuntime(store)
	now := time.Date(2026, 4, 23, 14, 0, 0, 0, time.UTC)
	runtime.now = func() time.Time { return now }
	ctx := context.Background()
	initial := NewWorkflow(
		`wf-call-job-runtime`,
		WorkflowStep{Kind: WorkflowStepKindPersist, Name: `greeting`, Value: `hello`},
		WorkflowStep{Kind: WorkflowStepKindCallJob, Name: `child-job`, Value: `job-child-1`},
		WorkflowStep{Kind: WorkflowStepKindCompensate, Name: `compensate`},
	)

	started, err := runtime.StartOrResume(testWorkflowContext(ctx, `plugin-workflow-demo`, `start-child-job`), `wf-call-job-runtime`, `plugin-workflow-demo`, `message.received`, `evt-start-child-job`, initial)
	if err != nil {
		t.Fatalf(`start workflow: %v`, err)
	}
	if !started.Started || started.Instance.Status != WorkflowRuntimeStatusWaitingJob {
		t.Fatalf(`expected waiting_job workflow after start, got %+v`, started)
	}
	if started.Instance.Workflow.WaitingForJob == nil || started.Instance.Workflow.WaitingForJob.JobID != `job-child-1` {
		t.Fatalf(`expected persisted waiting child job state, got %+v`, started.Instance.Workflow)
	}

	persistedStarted, err := store.LoadWorkflowInstance(ctx, `wf-call-job-runtime`)
	if err != nil {
		t.Fatalf(`load started workflow: %v`, err)
	}
	if persistedStarted.Status != WorkflowRuntimeStatusWaitingJob || persistedStarted.Workflow.WaitingForJob == nil || persistedStarted.Workflow.WaitingForJob.JobID != `job-child-1` {
		t.Fatalf(`expected persisted waiting_job state, got %+v`, persistedStarted)
	}
	if persistedStarted.LastEventID != `evt-start-child-job` || persistedStarted.LastEventType != `message.received` {
		t.Fatalf(`expected start event cursor to remain on ingress event, got %+v`, persistedStarted)
	}

	resumed, err := runtime.ResumeFromChildJob(ctx, `wf-call-job-runtime`, `plugin-workflow-demo`, WorkflowChildJobResume{JobID: `job-child-1`, Status: JobStatusDone})
	if err != nil {
		t.Fatalf(`resume from child job: %v`, err)
	}
	if !resumed.Resumed || resumed.Instance.Status != WorkflowRuntimeStatusCompleted {
		t.Fatalf(`expected resumed completed workflow after successful child job, got %+v`, resumed)
	}
	if resumed.Instance.Workflow.Compensated {
		t.Fatalf(`expected successful child job not to trigger compensation, got %+v`, resumed.Instance.Workflow)
	}
	jobState, ok := resumed.Instance.Workflow.State[`child-job`].(map[string]any)
	if !ok || jobState[`job_id`] != `job-child-1` || jobState[`status`] != string(JobStatusDone) {
		t.Fatalf(`expected successful child job result state, got %+v`, resumed.Instance.Workflow.State[`child-job`])
	}

	persistedResumed, err := store.LoadWorkflowInstance(ctx, `wf-call-job-runtime`)
	if err != nil {
		t.Fatalf(`load resumed workflow: %v`, err)
	}
	if persistedResumed.Status != WorkflowRuntimeStatusCompleted || !persistedResumed.Workflow.Completed || persistedResumed.Workflow.WaitingForJob != nil {
		t.Fatalf(`expected persisted completed workflow after child resume, got %+v`, persistedResumed)
	}
	if persistedResumed.LastEventID != `job.result:job-child-1` || persistedResumed.LastEventType != `job.result` {
		t.Fatalf(`expected child-job resume boundary to avoid ingress event cursor rewrite, got %+v`, persistedResumed)
	}
	if persistedResumed.TraceID != `trace-start-child-job` || persistedResumed.EventID != `evt-start-child-job` || persistedResumed.RunID != `run-start-child-job` || persistedResumed.CorrelationID != `corr-start-child-job` {
		t.Fatalf(`expected origin observability ids to stay stable after child-job resume, got %+v`, persistedResumed)
	}
	if _, err := runtime.StartOrResume(testWorkflowContext(ctx, `plugin-workflow-demo`, `event-during-child-wait`), `wf-call-job-runtime`, `plugin-workflow-demo`, `message.received`, `evt-during-child-wait`, Workflow{}); err == nil {
		t.Fatal(`expected ingress resume to be rejected while workflow waits for child job result`)
	}

	metricsOutput := runtime.metrics.RenderPrometheus()
	for _, expected := range []string{
		`bot_platform_workflow_transition_total{plugin_id="plugin-workflow-demo",outcome="started"} 1`,
		`bot_platform_workflow_transition_total{plugin_id="plugin-workflow-demo",outcome="resumed"} 1`,
		`bot_platform_workflow_instance_count{status="waiting_job"} 0`,
		`bot_platform_workflow_instance_count{status="completed"} 1`,
	} {
		if !strings.Contains(metricsOutput, expected) {
			t.Fatalf(`expected workflow metrics output to contain %q, got %s`, expected, metricsOutput)
		}
	}
}

func TestWorkflowRuntimeResumeFromChildJobFailureTriggersCompensationAndRestoreContinuity(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	createdAt := time.Date(2026, 4, 24, 9, 0, 0, 0, time.UTC)
	workflow := NewWorkflow(
		`wf-child-job-restore`,
		WorkflowStep{Kind: WorkflowStepKindPersist, Name: `greeting`, Value: `hello`},
		WorkflowStep{Kind: WorkflowStepKindCallJob, Name: `child-job`, Value: `job-child-dead`},
		WorkflowStep{Kind: WorkflowStepKindCompensate, Name: `compensate`},
	)
	workflow.State[`greeting`] = `hello`
	workflow.CurrentIndex = 1
	workflow.WaitingForJob = &WorkflowCallJobState{StepName: `child-job`, JobID: `job-child-dead`}

	if err := store.SaveWorkflowInstance(ctx, WorkflowInstanceState{
		WorkflowID:    `wf-child-job-restore`,
		PluginID:      `plugin-workflow-demo`,
		TraceID:       `trace-child-job-restore`,
		EventID:       `evt-child-job-restore-origin`,
		RunID:         `run-child-job-restore`,
		CorrelationID: `corr-child-job-restore`,
		Status:        WorkflowRuntimeStatusWaitingJob,
		Workflow:      workflow,
		LastEventID:   `evt-child-job-restore-last`,
		LastEventType: `message.received`,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}); err != nil {
		t.Fatalf(`save waiting child-job workflow: %v`, err)
	}

	restarted := NewWorkflowRuntime(store)
	restarted.now = func() time.Time { return createdAt.Add(5 * time.Minute) }
	if err := restarted.Restore(ctx); err != nil {
		t.Fatalf(`restore workflow runtime: %v`, err)
	}
	snapshot := restarted.LastRecoverySnapshot()
	if snapshot.TotalWorkflows != 1 || snapshot.RecoveredWorkflows != 1 || snapshot.StatusCounts[WorkflowRuntimeStatusWaitingJob] != 1 {
		t.Fatalf(`expected waiting_job workflow to survive restore unchanged, got %+v`, snapshot)
	}
	restored, err := restarted.Load(ctx, `wf-child-job-restore`)
	if err != nil {
		t.Fatalf(`load restored workflow: %v`, err)
	}
	if restored.Status != WorkflowRuntimeStatusWaitingJob || restored.Workflow.WaitingForJob == nil || restored.Workflow.WaitingForJob.JobID != `job-child-dead` {
		t.Fatalf(`expected restored workflow to remain blocked on child job, got %+v`, restored)
	}

	resumed, err := restarted.ResumeFromChildJob(ctx, `wf-child-job-restore`, `plugin-workflow-demo`, WorkflowChildJobResume{JobID: `job-child-dead`, Status: JobStatusDead, ReasonCode: JobReasonCodeExecutionDead, LastError: `boom`})
	if err != nil {
		t.Fatalf(`resume from failed child job: %v`, err)
	}
	if !resumed.Resumed || resumed.Instance.Status != WorkflowRuntimeStatusCompleted || !resumed.Instance.Workflow.Compensated {
		t.Fatalf(`expected failed child job to complete with compensation, got %+v`, resumed)
	}
	jobState, ok := resumed.Instance.Workflow.State[`child-job`].(map[string]any)
	if !ok || jobState[`reason_code`] != string(JobReasonCodeExecutionDead) || jobState[`last_error`] != `boom` {
		t.Fatalf(`expected failed child job state to persist reason and error, got %+v`, resumed.Instance.Workflow.State[`child-job`])
	}
	if resumed.Instance.LastEventID != `job.result:job-child-dead` || resumed.Instance.LastEventType != `job.result` {
		t.Fatalf(`expected child-job resume not to rewrite last ingress event id, got %+v`, resumed.Instance)
	}
	if _, err := restarted.ResumeFromChildJob(ctx, `wf-child-job-restore`, `plugin-workflow-demo`, WorkflowChildJobResume{JobID: `job-child-dead`, Status: JobStatusDone}); err == nil {
		t.Fatal(`expected child-job resume to reject already-completed workflow boundary`)
	}
}

func TestWorkflowRuntimeResumeFromChildJobRejectsNonTerminalAndWrongJobID(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	runtime := NewWorkflowRuntime(store)
	runtime.now = func() time.Time { return time.Date(2026, 4, 24, 11, 0, 0, 0, time.UTC) }
	ctx := context.Background()
	initial := NewWorkflow(
		`wf-child-job-errors`,
		WorkflowStep{Kind: WorkflowStepKindCallJob, Name: `child-job`, Value: `job-child-errors`},
	)
	if _, err := runtime.StartOrResume(testWorkflowContext(ctx, `plugin-workflow-demo`, `start-child-job-errors`), `wf-child-job-errors`, `plugin-workflow-demo`, `message.received`, `evt-start-child-job-errors`, initial); err != nil {
		t.Fatalf(`start workflow: %v`, err)
	}
	if _, err := runtime.ResumeFromChildJob(ctx, `wf-child-job-errors`, `plugin-workflow-demo`, WorkflowChildJobResume{JobID: `job-child-errors`, Status: JobStatusRunning}); err == nil {
		t.Fatal(`expected non-terminal child status to be rejected`)
	}
	if _, err := runtime.ResumeFromChildJob(ctx, `wf-child-job-errors`, `plugin-workflow-demo`, WorkflowChildJobResume{JobID: `job-unexpected`, Status: JobStatusDone}); err == nil {
		t.Fatal(`expected wrong child job id to be rejected`)
	}
	persisted, err := store.LoadWorkflowInstance(ctx, `wf-child-job-errors`)
	if err != nil {
		t.Fatalf(`load workflow after rejected child resumes: %v`, err)
	}
	if persisted.Status != WorkflowRuntimeStatusWaitingJob || persisted.Workflow.WaitingForJob == nil || persisted.Workflow.LastJobResult != nil {
		t.Fatalf(`expected rejected child resumes to leave workflow waiting state unchanged, got %+v`, persisted)
	}
}

func TestWorkflowRuntimeRestoreReconcilesWaitingChildJobWithPersistedTerminalResult(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	now := time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)
	workflow := NewWorkflow(
		`wf-child-job-reconcile`,
		WorkflowStep{Kind: WorkflowStepKindPersist, Name: `greeting`, Value: `hello`},
		WorkflowStep{Kind: WorkflowStepKindCallJob, Name: `child-job`, Value: `job-child-reconcile`},
		WorkflowStep{Kind: WorkflowStepKindCompensate, Name: `compensate`},
	)
	workflow.State[`greeting`] = `hello`
	workflow.CurrentIndex = 1
	workflow.WaitingForJob = &WorkflowCallJobState{StepName: `child-job`, JobID: `job-child-reconcile`}
	if err := store.SaveWorkflowInstance(ctx, WorkflowInstanceState{
		WorkflowID:    `wf-child-job-reconcile`,
		PluginID:      `plugin-workflow-demo`,
		TraceID:       `trace-child-job-reconcile`,
		EventID:       `evt-child-job-reconcile-origin`,
		RunID:         `run-child-job-reconcile`,
		CorrelationID: `corr-child-job-reconcile`,
		Status:        WorkflowRuntimeStatusWaitingJob,
		Workflow:      workflow,
		LastEventID:   `evt-child-job-reconcile-ingress`,
		LastEventType: `message.received`,
		CreatedAt:     now.Add(-time.Minute),
		UpdatedAt:     now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf(`save waiting child-job workflow: %v`, err)
	}
	terminalJob := NewJob(`job-child-reconcile`, `ai.chat`, 0, 30*time.Second)
	terminalJob.TraceID = `trace-child-job-reconcile`
	terminalJob.EventID = `evt-child-job-reconcile-origin`
	terminalJob.RunID = `run-child-job-reconcile`
	terminalJob.Correlation = `corr-child-job-reconcile`
	terminalJob.Status = JobStatusCancelled
	terminalJob.ReasonCode = JobReasonCodeTimeout
	terminalJob.LastError = `cancelled during crash window`
	finishedAt := now.Add(-30 * time.Second)
	terminalJob.FinishedAt = &finishedAt
	if err := store.SaveJob(ctx, terminalJob); err != nil {
		t.Fatalf(`save terminal child job: %v`, err)
	}

	runtime := NewWorkflowRuntime(store)
	runtime.now = func() time.Time { return now }
	if err := runtime.Restore(ctx); err != nil {
		t.Fatalf(`restore workflow runtime: %v`, err)
	}
	restored, err := runtime.Load(ctx, `wf-child-job-reconcile`)
	if err != nil {
		t.Fatalf(`load reconciled workflow: %v`, err)
	}
	if restored.Status != WorkflowRuntimeStatusCompleted || !restored.Workflow.Completed || !restored.Workflow.Compensated {
		t.Fatalf(`expected restore-time reconciliation to complete workflow with compensation, got %+v`, restored)
	}
	if restored.LastEventID != `job.result:job-child-reconcile` || restored.LastEventType != `job.result` {
		t.Fatalf(`expected reconciled restore checkpoint to point at child job result, got %+v`, restored)
	}
	jobState, ok := restored.Workflow.State[`child-job`].(map[string]any)
	if !ok || jobState[`status`] != string(JobStatusCancelled) || jobState[`reason_code`] != string(JobReasonCodeTimeout) {
		t.Fatalf(`expected reconciled restore to persist terminal child result, got %+v`, restored.Workflow.State[`child-job`])
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
