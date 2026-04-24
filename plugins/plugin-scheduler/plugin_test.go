package pluginscheduler

import (
	"errors"
	"testing"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
	runtimecore "github.com/ohmyopencode/bot-platform/packages/runtime-core"
)

type recordingReplyService struct {
	text string
	err  error
}

func (r *recordingReplyService) ReplyText(handle eventmodel.ReplyHandle, text string) error {
	r.text = text
	return r.err
}

func (r *recordingReplyService) ReplyImage(handle eventmodel.ReplyHandle, imageURL string) error {
	return nil
}
func (r *recordingReplyService) ReplyFile(handle eventmodel.ReplyHandle, fileURL string) error {
	return nil
}

type schedulerAdapter struct{ inner *runtimecore.Scheduler }

func (s schedulerAdapter) Register(plan pluginsdk.SchedulePlan) error {
	return s.inner.Register(runtimecore.SchedulePlan{
		ID:        plan.ID,
		Kind:      runtimecore.ScheduleKind(plan.Kind),
		CronExpr:  plan.CronExpr,
		Delay:     plan.Delay,
		ExecuteAt: plan.ExecuteAt,
		Source:    plan.Source,
		EventType: plan.EventType,
		Metadata:  plan.Metadata,
	})
}
func (s schedulerAdapter) Plan(id string) (pluginsdk.SchedulePlan, error) {
	plan, err := s.inner.Plan(id)
	if err != nil {
		return pluginsdk.SchedulePlan{}, err
	}
	return pluginsdk.SchedulePlan{
		ID:        plan.ID,
		Kind:      pluginsdk.ScheduleKind(plan.Kind),
		CronExpr:  plan.CronExpr,
		Delay:     plan.Delay,
		ExecuteAt: plan.ExecuteAt,
		Source:    plan.Source,
		EventType: plan.EventType,
		Metadata:  plan.Metadata,
	}, nil
}
func (s schedulerAdapter) Trigger(id string) (eventmodel.Event, error) { return s.inner.Trigger(id) }
func (s schedulerAdapter) Cancel(id string) error                      { return s.inner.Cancel(id) }
func (s schedulerAdapter) Plans() []pluginsdk.SchedulePlan {
	plans := s.inner.Plans()
	items := make([]pluginsdk.SchedulePlan, 0, len(plans))
	for _, plan := range plans {
		items = append(items, pluginsdk.SchedulePlan{
			ID:        plan.ID,
			Kind:      pluginsdk.ScheduleKind(plan.Kind),
			CronExpr:  plan.CronExpr,
			Delay:     plan.Delay,
			ExecuteAt: plan.ExecuteAt,
			Source:    plan.Source,
			EventType: plan.EventType,
			Metadata:  plan.Metadata,
		})
	}
	return items
}

func TestPluginSchedulerSupportsCronDelayListAndCancel(t *testing.T) {
	t.Parallel()

	scheduler := runtimecore.NewScheduler()
	plugin := New(schedulerAdapter{inner: scheduler}, &recordingReplyService{})

	if err := plugin.CreateCronPlan("cron-demo", "0 * * * *", "cron message"); err != nil {
		t.Fatalf("create cron plan: %v", err)
	}
	if err := plugin.CreateDelayPlan("delay-demo", 30, "delay message"); err != nil {
		t.Fatalf("create delay plan: %v", err)
	}

	ids := plugin.ListPlanIDs()
	if len(ids) != 2 || ids[0] != "cron-demo" || ids[1] != "delay-demo" {
		t.Fatalf("unexpected plan ids %+v", ids)
	}

	if err := plugin.CancelPlan("cron-demo"); err != nil {
		t.Fatalf("cancel plan: %v", err)
	}
	ids = plugin.ListPlanIDs()
	if len(ids) != 1 || ids[0] != "delay-demo" {
		t.Fatalf("unexpected plan ids after cancel %+v", ids)
	}
}

func TestPluginSchedulerRepliesWhenTriggered(t *testing.T) {
	t.Parallel()

	replies := &recordingReplyService{}
	plugin := New(schedulerAdapter{inner: runtimecore.NewScheduler()}, replies)

	err := plugin.OnEvent(eventmodel.Event{
		EventID:        "evt-scheduler",
		TraceID:        "trace-scheduler",
		Source:         "scheduler",
		Type:           "schedule.triggered",
		Timestamp:      time.Date(2026, 4, 2, 23, 0, 0, 0, time.UTC),
		IdempotencyKey: "schedule:plugin",
		System:         &eventmodel.SystemEvent{Name: "delay-demo", Status: "triggered"},
		Metadata:       map[string]any{"message": "scheduled hello"},
	}, eventmodel.ExecutionContext{
		TraceID: "trace-scheduler",
		EventID: "evt-scheduler",
		Reply:   &eventmodel.ReplyHandle{Capability: "onebot.reply", TargetID: "group-42"},
	})
	if err != nil {
		t.Fatalf("on event: %v", err)
	}
	if replies.text != "scheduled hello" {
		t.Fatalf("unexpected reply text %q", replies.text)
	}
}

func TestPluginSchedulerReturnsReplyError(t *testing.T) {
	t.Parallel()

	plugin := New(schedulerAdapter{inner: runtimecore.NewScheduler()}, &recordingReplyService{err: errors.New("send failed")})
	err := plugin.OnEvent(eventmodel.Event{
		EventID:        "evt-scheduler",
		TraceID:        "trace-scheduler",
		Source:         "scheduler",
		Type:           "schedule.triggered",
		Timestamp:      time.Date(2026, 4, 2, 23, 0, 0, 0, time.UTC),
		IdempotencyKey: "schedule:plugin",
		System:         &eventmodel.SystemEvent{Name: "delay-demo", Status: "triggered"},
	}, eventmodel.ExecutionContext{
		TraceID: "trace-scheduler",
		EventID: "evt-scheduler",
		Reply:   &eventmodel.ReplyHandle{Capability: "onebot.reply", TargetID: "group-42"},
	})
	if err == nil {
		t.Fatal("expected reply error to bubble up")
	}
}

func TestPluginSchedulerManifestAdoptsV1Contract(t *testing.T) {
	t.Parallel()

	plugin := New(schedulerAdapter{inner: runtimecore.NewScheduler()}, &recordingReplyService{})
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
	if manifest.Publish.SourceURI != pluginSchedulerPublishSourceURI {
		t.Fatalf("manifest publish sourceUri = %q, want %q", manifest.Publish.SourceURI, pluginSchedulerPublishSourceURI)
	}
	if manifest.Publish.RuntimeVersionRange != pluginSchedulerRuntimeVersionRange {
		t.Fatalf("manifest publish runtimeVersionRange = %q, want %q", manifest.Publish.RuntimeVersionRange, pluginSchedulerRuntimeVersionRange)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("expected plugin-scheduler manifest to validate, got %v", err)
	}
}
