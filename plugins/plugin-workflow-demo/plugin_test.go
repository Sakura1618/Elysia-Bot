package pluginworkflowdemo

import (
	"context"
	"path/filepath"
	"testing"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
)

type replyRecorder struct {
	texts []string
}

func (r *replyRecorder) ReplyText(handle eventmodel.ReplyHandle, text string) error {
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

	firstReplies := &replyRecorder{}
	firstPlugin := New(firstReplies, runtime)
	first := eventmodel.Event{
		EventID:        `evt-1`,
		TraceID:        `trace-1`,
		Source:         `runtime-workflow-demo`,
		Type:           `message.received`,
		IdempotencyKey: `msg:1`,
		Actor:          &eventmodel.Actor{ID: `user-1`, Type: `user`},
		Message:        &eventmodel.Message{Text: `start workflow`},
	}
	ctx1 := eventmodel.ExecutionContext{TraceID: `trace-1`, EventID: `evt-1`, Reply: &eventmodel.ReplyHandle{Capability: `onebot.reply`, TargetID: `group-42`}}
	if err := firstPlugin.OnEvent(first, ctx1); err != nil {
		t.Fatalf(`start workflow: %v`, err)
	}
	if len(firstReplies.texts) != 1 || firstReplies.texts[0] != `workflow started, please send another message to continue` {
		t.Fatalf(`unexpected first replies %+v`, firstReplies.texts)
	}
	stored, err := store.LoadWorkflowInstance(ctx, `workflow-user-1`)
	if err != nil {
		t.Fatalf(`load started workflow: %v`, err)
	}
	if stored.PluginID != `plugin-workflow-demo` || stored.Status != runtimecore.WorkflowRuntimeStatusWaitingEvent {
		t.Fatalf(`expected waiting runtime-owned workflow state, got %+v`, stored)
	}
	if stored.Workflow.WaitingFor != `message.received` || stored.Workflow.State[`greeting`] != `start workflow` || stored.Workflow.Completed {
		t.Fatalf(`expected persisted workflow waiting state, got %+v`, stored.Workflow)
	}

	restartedRuntime := runtimecore.NewWorkflowRuntime(store)
	if err := restartedRuntime.Restore(ctx); err != nil {
		t.Fatalf(`restore workflow runtime: %v`, err)
	}
	snapshot := restartedRuntime.LastRecoverySnapshot()
	if snapshot.TotalWorkflows != 1 || snapshot.RecoveredWorkflows != 1 || snapshot.StatusCounts[runtimecore.WorkflowRuntimeStatusWaitingEvent] != 1 {
		t.Fatalf(`expected workflow recovery snapshot after restart, got %+v`, snapshot)
	}
	secondReplies := &replyRecorder{}
	secondPlugin := New(secondReplies, restartedRuntime)
	second := eventmodel.Event{
		EventID:        `evt-2`,
		TraceID:        `trace-2`,
		Source:         `runtime-workflow-demo`,
		Type:           `message.received`,
		IdempotencyKey: `msg:2`,
		Actor:          &eventmodel.Actor{ID: `user-1`, Type: `user`},
		Message:        &eventmodel.Message{Text: `continue`},
	}
	ctx2 := eventmodel.ExecutionContext{TraceID: `trace-2`, EventID: `evt-2`, Reply: &eventmodel.ReplyHandle{Capability: `onebot.reply`, TargetID: `group-42`}}
	if err := secondPlugin.OnEvent(second, ctx2); err != nil {
		t.Fatalf(`resume workflow: %v`, err)
	}
	if len(secondReplies.texts) != 1 || secondReplies.texts[0] != `workflow resumed and completed` {
		t.Fatalf(`unexpected second replies %+v`, secondReplies.texts)
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
}
