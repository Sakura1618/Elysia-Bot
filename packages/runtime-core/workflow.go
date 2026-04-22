package runtimecore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type WorkflowStepKind = pluginsdk.WorkflowStepKind

const (
	WorkflowStepKindStep       = pluginsdk.WorkflowStepKindStep
	WorkflowStepKindWaitEvent  = pluginsdk.WorkflowStepKindWaitEvent
	WorkflowStepKindSleep      = pluginsdk.WorkflowStepKindSleep
	WorkflowStepKindCallJob    = pluginsdk.WorkflowStepKindCallJob
	WorkflowStepKindPersist    = pluginsdk.WorkflowStepKindPersist
	WorkflowStepKindCompensate = pluginsdk.WorkflowStepKindCompensate
)

type WorkflowStep = pluginsdk.WorkflowStep
type Workflow = pluginsdk.Workflow

func NewWorkflow(id string, steps ...WorkflowStep) Workflow {
	return pluginsdk.NewWorkflow(id, steps...)
}

type WorkflowRuntimeStatus string

const (
	WorkflowRuntimeStatusReady        WorkflowRuntimeStatus = `ready`
	WorkflowRuntimeStatusWaitingEvent WorkflowRuntimeStatus = `waiting_event`
	WorkflowRuntimeStatusSleeping     WorkflowRuntimeStatus = `sleeping`
	WorkflowRuntimeStatusCompleted    WorkflowRuntimeStatus = `completed`
)

type WorkflowInstanceState struct {
	WorkflowID    string
	PluginID      string
	Status        WorkflowRuntimeStatus
	Workflow      Workflow
	LastEventID   string
	LastEventType string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type WorkflowTransition struct {
	Instance WorkflowInstanceState
	Started  bool
	Resumed  bool
}

type WorkflowRecoverySnapshot struct {
	RecoveredAt        time.Time
	TotalWorkflows     int
	RecoveredWorkflows int
	StatusCounts       map[WorkflowRuntimeStatus]int
}

type workflowRuntimeStore interface {
	SaveWorkflowInstance(context.Context, WorkflowInstanceState) error
	LoadWorkflowInstance(context.Context, string) (WorkflowInstanceState, error)
	ListWorkflowInstances(context.Context) ([]WorkflowInstanceState, error)
}

type WorkflowRuntime struct {
	mu           sync.RWMutex
	store        workflowRuntimeStore
	now          func() time.Time
	instances    map[string]WorkflowInstanceState
	metrics      *MetricsRegistry
	lastRecovery WorkflowRecoverySnapshot
}

func NewWorkflowRuntime(store workflowRuntimeStore) *WorkflowRuntime {
	return &WorkflowRuntime{
		store:     store,
		now:       time.Now().UTC,
		instances: map[string]WorkflowInstanceState{},
		metrics:   NewMetricsRegistry(),
	}
}

func (r *WorkflowRuntime) SetMetrics(metrics *MetricsRegistry) {
	if r == nil || metrics == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metrics = metrics
}

func (r *WorkflowRuntime) Restore(ctx context.Context) error {
	if r == nil || r.store == nil {
		return nil
	}
	loaded, err := r.store.ListWorkflowInstances(ctx)
	if err != nil {
		return fmt.Errorf(`list workflow instances: %w`, err)
	}
	recoveredAt := r.now()
	restored := make(map[string]WorkflowInstanceState, len(loaded))
	snapshot := WorkflowRecoverySnapshot{
		RecoveredAt:  recoveredAt,
		StatusCounts: map[WorkflowRuntimeStatus]int{},
	}
	for _, instance := range loaded {
		restoredInstance, err := r.restoreInstance(ctx, instance, recoveredAt)
		if err != nil {
			return fmt.Errorf(`restore workflow %q: %w`, instance.WorkflowID, err)
		}
		restored[restoredInstance.WorkflowID] = cloneWorkflowInstanceState(restoredInstance)
		snapshot.TotalWorkflows++
		snapshot.RecoveredWorkflows++
		snapshot.StatusCounts[restoredInstance.Status]++
	}
	r.mu.Lock()
	r.instances = restored
	r.lastRecovery = snapshot
	r.syncMetricsLocked()
	r.mu.Unlock()
	return nil
}

func (r *WorkflowRuntime) StartOrResume(ctx context.Context, workflowID string, pluginID string, eventType string, eventID string, initial Workflow) (WorkflowTransition, error) {
	if r == nil {
		return WorkflowTransition{}, fmt.Errorf(`workflow runtime is required`)
	}
	if r.store == nil {
		return WorkflowTransition{}, fmt.Errorf(`workflow runtime store is required`)
	}
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == `` {
		return WorkflowTransition{}, fmt.Errorf(`workflow id is required`)
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == `` {
		return WorkflowTransition{}, fmt.Errorf(`plugin id is required`)
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == `` {
		return WorkflowTransition{}, fmt.Errorf(`event type is required`)
	}
	now := r.now()
	current, found, err := r.load(ctx, workflowID)
	if err != nil {
		return WorkflowTransition{}, fmt.Errorf(`load workflow instance: %w`, err)
	}
	if found {
		current.PluginID = strings.TrimSpace(current.PluginID)
		if current.PluginID == `` {
			return WorkflowTransition{}, fmt.Errorf(`workflow %q owner plugin is missing`, workflowID)
		}
		if current.PluginID != pluginID {
			return WorkflowTransition{}, fmt.Errorf(`workflow %q is owned by plugin %q, not %q`, workflowID, current.PluginID, pluginID)
		}
	}
	if !found || current.Workflow.Completed {
		initial.ID = workflowID
		if len(initial.Steps) == 0 {
			return WorkflowTransition{}, fmt.Errorf(`workflow %q requires at least one step`, workflowID)
		}
		driven, err := advanceWorkflowUntilBlocked(cloneWorkflow(initial), now)
		if err != nil {
			return WorkflowTransition{}, err
		}
		instance := WorkflowInstanceState{
			WorkflowID:    workflowID,
			PluginID:      pluginID,
			Status:        workflowRuntimeStatus(driven),
			Workflow:      driven,
			LastEventID:   strings.TrimSpace(eventID),
			LastEventType: eventType,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if err := r.save(ctx, instance); err != nil {
			return WorkflowTransition{}, fmt.Errorf(`save started workflow: %w`, err)
		}
		r.recordTransitionMetric(instance.PluginID, "started")
		return WorkflowTransition{Instance: cloneWorkflowInstanceState(instance), Started: true}, nil
	}

	updated := cloneWorkflowInstanceState(current)
	workflow := cloneWorkflow(updated.Workflow)
	if workflow.WaitingFor != `` {
		workflow, err = workflow.ResumeWithEvent(eventType)
		if err != nil {
			return WorkflowTransition{}, err
		}
	} else if workflow.SleepingUntil != nil {
		workflow, err = workflow.ResumeAfterSleep(now)
		if err != nil {
			return WorkflowTransition{}, err
		}
	} else {
		return WorkflowTransition{}, fmt.Errorf(`workflow %q is not waiting for a resumable trigger`, workflowID)
	}
	driven, err := advanceWorkflowUntilBlocked(workflow, now)
	if err != nil {
		return WorkflowTransition{}, err
	}
	updated.Workflow = driven
	updated.Status = workflowRuntimeStatus(driven)
	updated.LastEventID = strings.TrimSpace(eventID)
	updated.LastEventType = eventType
	updated.UpdatedAt = now
	if updated.CreatedAt.IsZero() {
		updated.CreatedAt = now
	}
	if err := r.save(ctx, updated); err != nil {
		return WorkflowTransition{}, fmt.Errorf(`save resumed workflow: %w`, err)
	}
	r.recordTransitionMetric(updated.PluginID, "resumed")
	return WorkflowTransition{Instance: cloneWorkflowInstanceState(updated), Resumed: true}, nil
}

func (r *WorkflowRuntime) Load(ctx context.Context, workflowID string) (WorkflowInstanceState, error) {
	instance, found, err := r.load(ctx, workflowID)
	if err != nil {
		return WorkflowInstanceState{}, err
	}
	if !found {
		return WorkflowInstanceState{}, sql.ErrNoRows
	}
	return instance, nil
}

func (r *WorkflowRuntime) List(ctx context.Context) ([]WorkflowInstanceState, error) {
	if r == nil {
		return nil, nil
	}
	if r.store == nil {
		r.mu.RLock()
		defer r.mu.RUnlock()
		items := make([]WorkflowInstanceState, 0, len(r.instances))
		for _, instance := range r.instances {
			items = append(items, cloneWorkflowInstanceState(instance))
		}
		return items, nil
	}
	loaded, err := r.store.ListWorkflowInstances(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]WorkflowInstanceState, 0, len(loaded))
	for _, instance := range loaded {
		items = append(items, cloneWorkflowInstanceState(instance))
	}
	return items, nil
}

func (r *WorkflowRuntime) LastRecoverySnapshot() WorkflowRecoverySnapshot {
	if r == nil {
		return WorkflowRecoverySnapshot{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneWorkflowRecoverySnapshot(r.lastRecovery)
}

func (r *WorkflowRuntime) load(ctx context.Context, workflowID string) (WorkflowInstanceState, bool, error) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == `` {
		return WorkflowInstanceState{}, false, nil
	}
	if r.store != nil {
		loaded, err := r.store.LoadWorkflowInstance(ctx, workflowID)
		if err == sql.ErrNoRows {
			return WorkflowInstanceState{}, false, nil
		}
		if err != nil {
			return WorkflowInstanceState{}, false, err
		}
		r.mu.Lock()
		r.instances[loaded.WorkflowID] = cloneWorkflowInstanceState(loaded)
		r.mu.Unlock()
		return loaded, true, nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	instance, ok := r.instances[workflowID]
	if !ok {
		return WorkflowInstanceState{}, false, nil
	}
	return cloneWorkflowInstanceState(instance), true, nil
}

func (r *WorkflowRuntime) save(ctx context.Context, instance WorkflowInstanceState) error {
	if r.store != nil {
		if err := r.store.SaveWorkflowInstance(ctx, instance); err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.instances[instance.WorkflowID] = cloneWorkflowInstanceState(instance)
	r.syncMetricsLocked()
	r.mu.Unlock()
	return nil
}

func (r *WorkflowRuntime) syncMetricsLocked() {
	if r == nil || r.metrics == nil {
		return
	}
	counts := map[WorkflowRuntimeStatus]int{
		WorkflowRuntimeStatusReady:        0,
		WorkflowRuntimeStatusWaitingEvent: 0,
		WorkflowRuntimeStatusSleeping:     0,
		WorkflowRuntimeStatusCompleted:    0,
	}
	for _, instance := range r.instances {
		counts[instance.Status]++
	}
	for status, count := range counts {
		r.metrics.SetWorkflowInstanceCount(status, count)
	}
}

func (r *WorkflowRuntime) recordTransitionMetric(pluginID string, outcome string) {
	r.mu.RLock()
	metrics := r.metrics
	r.mu.RUnlock()
	if metrics == nil {
		return
	}
	metrics.RecordWorkflowTransition(pluginID, outcome)
}

func (r *WorkflowRuntime) restoreInstance(ctx context.Context, instance WorkflowInstanceState, at time.Time) (WorkflowInstanceState, error) {
	restored := cloneWorkflowInstanceState(instance)
	restored.WorkflowID = strings.TrimSpace(restored.WorkflowID)
	restored.PluginID = strings.TrimSpace(restored.PluginID)
	if restored.WorkflowID == `` {
		return WorkflowInstanceState{}, fmt.Errorf(`workflow id is required`)
	}
	if restored.PluginID == `` {
		return WorkflowInstanceState{}, fmt.Errorf(`plugin id is required`)
	}
	restored.Workflow.ID = restored.WorkflowID
	driven, err := advanceWorkflowUntilBlocked(restored.Workflow, at)
	if err != nil {
		return WorkflowInstanceState{}, err
	}
	restored.Workflow = driven
	restored.Status = workflowRuntimeStatus(driven)
	if restored.CreatedAt.IsZero() {
		restored.CreatedAt = at
	}
	if restored.UpdatedAt.IsZero() {
		restored.UpdatedAt = restored.CreatedAt
	}
	if err := r.store.SaveWorkflowInstance(ctx, restored); err != nil {
		return WorkflowInstanceState{}, fmt.Errorf(`persist restored workflow: %w`, err)
	}
	return restored, nil
}

func cloneWorkflow(workflow Workflow) Workflow {
	cloned := workflow
	if len(workflow.Steps) > 0 {
		cloned.Steps = append([]WorkflowStep(nil), workflow.Steps...)
	}
	cloned.State = cloneWorkflowStateMap(workflow.State)
	if workflow.SleepingUntil != nil {
		sleepingUntil := workflow.SleepingUntil.UTC()
		cloned.SleepingUntil = &sleepingUntil
	}
	return cloned
}

func cloneWorkflowStateMap(state map[string]any) map[string]any {
	if len(state) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(state))
	for key, value := range state {
		cloned[key] = value
	}
	return cloned
}

func cloneWorkflowInstanceState(instance WorkflowInstanceState) WorkflowInstanceState {
	cloned := instance
	cloned.Workflow = cloneWorkflow(instance.Workflow)
	return cloned
}

func cloneWorkflowRecoverySnapshot(snapshot WorkflowRecoverySnapshot) WorkflowRecoverySnapshot {
	cloned := snapshot
	if len(snapshot.StatusCounts) > 0 {
		cloned.StatusCounts = make(map[WorkflowRuntimeStatus]int, len(snapshot.StatusCounts))
		for status, count := range snapshot.StatusCounts {
			cloned.StatusCounts[status] = count
		}
	}
	return cloned
}

func workflowRuntimeStatus(workflow Workflow) WorkflowRuntimeStatus {
	if workflow.Completed {
		return WorkflowRuntimeStatusCompleted
	}
	if workflow.WaitingFor != `` {
		return WorkflowRuntimeStatusWaitingEvent
	}
	if workflow.SleepingUntil != nil {
		return WorkflowRuntimeStatusSleeping
	}
	return WorkflowRuntimeStatusReady
}

func advanceWorkflowUntilBlocked(workflow Workflow, now time.Time) (Workflow, error) {
	current := cloneWorkflow(workflow)
	for {
		if current.Completed {
			return current, nil
		}
		if current.WaitingFor != `` {
			return current, nil
		}
		if current.SleepingUntil != nil {
			if current.SleepingUntil.After(now) {
				return current, nil
			}
			resumed, err := current.ResumeAfterSleep(now)
			if err != nil {
				return Workflow{}, err
			}
			current = resumed
			continue
		}
		next, err := current.Advance(now)
		if err != nil {
			return Workflow{}, err
		}
		current = next
	}
}
