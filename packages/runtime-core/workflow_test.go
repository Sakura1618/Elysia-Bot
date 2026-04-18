package runtimecore

import (
	"context"
	"testing"
	"time"
)

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
