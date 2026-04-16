package runtimecore

import (
	"context"
	"strings"
	"testing"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type schedulerRecordingHandler struct {
	called   bool
	typeSeen string
}

func (h *schedulerRecordingHandler) OnEvent(event eventmodel.Event, ctx eventmodel.ExecutionContext) error {
	h.called = true
	h.typeSeen = event.Type
	return nil
}

func TestSchedulerRegistersKindsAndTriggersStandardEvent(t *testing.T) {
	t.Parallel()

	scheduler := NewScheduler()
	scheduler.now = func() time.Time { return time.Date(2026, 4, 2, 21, 0, 0, 0, time.UTC) }

	plans := []SchedulePlan{
		{ID: "cron-demo", Kind: ScheduleKindCron, CronExpr: "0 * * * *", Source: "scheduler", EventType: "schedule.triggered"},
		{ID: "delay-demo", Kind: ScheduleKindDelay, Delay: time.Minute, Source: "scheduler", EventType: "schedule.triggered"},
		{ID: "oneshot-demo", Kind: ScheduleKindOneShot, ExecuteAt: time.Date(2026, 4, 2, 22, 0, 0, 0, time.UTC), Source: "scheduler", EventType: "schedule.triggered"},
	}

	for _, plan := range plans {
		if err := scheduler.Register(plan); err != nil {
			t.Fatalf("register schedule %s: %v", plan.ID, err)
		}
	}

	event, err := scheduler.Trigger("cron-demo")
	if err != nil {
		t.Fatalf("trigger schedule: %v", err)
	}
	if event.Source != "scheduler" || event.Type != "schedule.triggered" || event.System == nil || event.System.Name != "cron-demo" {
		t.Fatalf("unexpected scheduled event: %+v", event)
	}
}

func TestSchedulerOutputCanUseSameDispatchChain(t *testing.T) {
	t.Parallel()

	handler := &schedulerRecordingHandler{}
	runtime := NewInMemoryRuntime(NoopSupervisor{}, DirectPluginHost{})
	if err := runtime.RegisterPlugin(pluginsdk.Plugin{
		Manifest: pluginsdk.PluginManifest{
			ID:         "plugin-scheduler-demo",
			Name:       "Scheduler Demo Plugin",
			Version:    "0.1.0",
			APIVersion: "v0",
			Mode:       pluginsdk.ModeSubprocess,
			Entry:      pluginsdk.PluginEntry{Module: "plugins/scheduler-demo", Symbol: "Plugin"},
		},
		Handlers: pluginsdk.Handlers{Event: handler},
	}); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	scheduler := NewScheduler()
	scheduler.now = func() time.Time { return time.Date(2026, 4, 2, 21, 0, 0, 0, time.UTC) }
	if err := scheduler.Register(SchedulePlan{ID: "delay-demo", Kind: ScheduleKindDelay, Delay: time.Minute, Source: "scheduler", EventType: "schedule.triggered"}); err != nil {
		t.Fatalf("register schedule: %v", err)
	}

	event, err := scheduler.Trigger("delay-demo")
	if err != nil {
		t.Fatalf("trigger schedule: %v", err)
	}
	if err := runtime.DispatchEvent(context.Background(), event); err != nil {
		t.Fatalf("dispatch scheduled event: %v", err)
	}
	if !handler.called || handler.typeSeen != "schedule.triggered" {
		t.Fatalf("expected scheduled event to go through dispatch chain, got %+v", handler)
	}
}

func TestSchedulerRejectsInvalidPlan(t *testing.T) {
	t.Parallel()

	scheduler := NewScheduler()
	if err := scheduler.Register(SchedulePlan{ID: "broken", Kind: ScheduleKindDelay, Source: "scheduler", EventType: "schedule.triggered"}); err == nil {
		t.Fatal("expected invalid delay plan to fail")
	}
	if err := scheduler.Register(SchedulePlan{ID: "broken-cron", Kind: ScheduleKindCron, CronExpr: "not a cron", Source: "scheduler", EventType: "schedule.triggered"}); err == nil {
		t.Fatal("expected invalid cron plan to fail")
	}
}

func TestSchedulerRunnerAutomaticallyFiresDelayPlanOnce(t *testing.T) {
	t.Parallel()

	scheduler := NewScheduler()
	start := time.Date(2026, 4, 4, 9, 0, 0, 0, time.UTC)
	current := start
	scheduler.now = func() time.Time { return current }
	if err := scheduler.Register(SchedulePlan{ID: "delay-auto", Kind: ScheduleKindDelay, Delay: 20 * time.Millisecond, Source: "scheduler", EventType: "schedule.triggered"}); err != nil {
		t.Fatalf("register delay plan: %v", err)
	}

	events := make(chan eventmodel.Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := scheduler.Start(ctx, 5*time.Millisecond, func(event eventmodel.Event) error {
		events <- event
		return nil
	}); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}

	current = start.Add(25 * time.Millisecond)
	select {
	case event := <-events:
		if event.System == nil || event.System.Name != "delay-auto" {
			t.Fatalf("unexpected auto-fired event %+v", event)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected delay plan to auto-fire")
	}

	select {
	case event := <-events:
		t.Fatalf("expected delay plan to fire only once, got %+v", event)
	case <-time.After(30 * time.Millisecond):
	}
	if _, err := scheduler.Plan("delay-auto"); err == nil {
		t.Fatal("expected fired delay plan to be removed")
	}
}

func TestSchedulerRunnerAutomaticallyFiresOneShotPlanAndStopsCleanly(t *testing.T) {
	t.Parallel()

	scheduler := NewScheduler()
	start := time.Date(2026, 4, 4, 10, 0, 0, 0, time.UTC)
	current := start
	scheduler.now = func() time.Time { return current }
	if err := scheduler.Register(SchedulePlan{ID: "oneshot-auto", Kind: ScheduleKindOneShot, ExecuteAt: start.Add(30 * time.Millisecond), Source: "scheduler", EventType: "schedule.triggered"}); err != nil {
		t.Fatalf("register one-shot plan: %v", err)
	}

	events := make(chan eventmodel.Event, 2)
	ctx, cancel := context.WithCancel(context.Background())
	if err := scheduler.Start(ctx, 5*time.Millisecond, func(event eventmodel.Event) error {
		events <- event
		return nil
	}); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	current = start.Add(35 * time.Millisecond)
	select {
	case <-events:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected one-shot plan to auto-fire")
	}
	cancel()
	deadline := time.Now().Add(200 * time.Millisecond)
	for scheduler.Running() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if scheduler.Running() {
		t.Fatal("expected scheduler to stop cleanly after context cancel")
	}
}

func TestSchedulerRunnerAutomaticallyFiresCronPlanAndReschedules(t *testing.T) {
	t.Parallel()

	scheduler := NewScheduler()
	current := time.Date(2026, 4, 4, 11, 0, 0, 0, time.UTC)
	scheduler.now = func() time.Time { return current }
	if err := scheduler.Register(SchedulePlan{ID: "cron-auto", Kind: ScheduleKindCron, CronExpr: "* * * * *", Source: "scheduler", EventType: "schedule.triggered"}); err != nil {
		t.Fatalf("register cron plan: %v", err)
	}

	events := make(chan eventmodel.Event, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := scheduler.Start(ctx, 5*time.Millisecond, func(event eventmodel.Event) error {
		events <- event
		return nil
	}); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	current = current.Add(time.Minute)
	select {
	case event := <-events:
		if event.System == nil || event.System.Name != "cron-auto" {
			t.Fatalf("unexpected cron event %+v", event)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected cron plan to auto-fire")
	}
	if _, err := scheduler.Plan("cron-auto"); err != nil {
		t.Fatalf("expected cron plan to remain registered after firing: %v", err)
	}

	current = current.Add(time.Minute)
	select {
	case event := <-events:
		if event.System == nil || event.System.Name != "cron-auto" {
			t.Fatalf("unexpected rescheduled cron event %+v", event)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected cron plan to auto-fire again after reschedule")
	}
}

func TestSchedulerRunnerRetriesOneShotAfterDispatchFailure(t *testing.T) {
	t.Parallel()

	scheduler := NewScheduler()
	start := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	current := start
	scheduler.now = func() time.Time { return current }
	if err := scheduler.Register(SchedulePlan{ID: "oneshot-fail", Kind: ScheduleKindOneShot, ExecuteAt: start.Add(10 * time.Millisecond), Source: "scheduler", EventType: "schedule.triggered"}); err != nil {
		t.Fatalf("register one-shot plan: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	if err := scheduler.Start(ctx, 5*time.Millisecond, func(event eventmodel.Event) error {
		attempts++
		if attempts == 1 {
			return context.Canceled
		}
		return nil
	}); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	current = start.Add(20 * time.Millisecond)
	time.Sleep(80 * time.Millisecond)
	if attempts < 2 {
		t.Fatalf("expected failed one-shot dispatch to be retried, got %d attempts", attempts)
	}
	if _, err := scheduler.Plan("oneshot-fail"); err == nil {
		t.Fatal("expected one-shot plan to be removed only after successful dispatch")
	}
}

func TestSchedulerRunnerDoesNotFireAfterCancelBeforeDue(t *testing.T) {
	t.Parallel()

	scheduler := NewScheduler()
	start := time.Date(2026, 4, 4, 13, 0, 0, 0, time.UTC)
	current := start
	scheduler.now = func() time.Time { return current }
	if err := scheduler.Register(SchedulePlan{ID: "delay-cancel", Kind: ScheduleKindDelay, Delay: 40 * time.Millisecond, Source: "scheduler", EventType: "schedule.triggered"}); err != nil {
		t.Fatalf("register delay plan: %v", err)
	}

	events := make(chan eventmodel.Event, 1)
	ctx, cancel := context.WithCancel(context.Background())
	if err := scheduler.Start(ctx, 5*time.Millisecond, func(event eventmodel.Event) error {
		events <- event
		return nil
	}); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	cancel()
	current = start.Add(60 * time.Millisecond)
	select {
	case event := <-events:
		t.Fatalf("expected cancelled runner not to fire late event, got %+v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSchedulerPersistsRegisterInspectListAndCancelThroughStore(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	registeredAt := time.Date(2026, 4, 8, 9, 0, 0, 0, time.UTC)
	scheduler := NewScheduler()
	scheduler.now = func() time.Time { return registeredAt }
	scheduler.SetStore(store)

	plan := SchedulePlan{
		ID:        "schedule-store-demo",
		Kind:      ScheduleKindDelay,
		Delay:     45 * time.Second,
		Source:    "runtime-demo-scheduler",
		EventType: "message.received",
		Metadata:  map[string]any{"message_text": "hello from store"},
	}
	if err := scheduler.Register(plan); err != nil {
		t.Fatalf("register schedule with store: %v", err)
	}

	stored, err := store.LoadSchedulePlan(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("load persisted schedule: %v", err)
	}
	if stored.Plan.ID != plan.ID || stored.Plan.Delay != plan.Delay || stored.DueAt == nil || !stored.DueAt.Equal(registeredAt.Add(plan.Delay)) {
		t.Fatalf("expected persisted schedule plan and dueAt, got %+v", stored)
	}

	fresh := NewScheduler()
	fresh.SetStore(store)
	loaded, err := fresh.Plan(plan.ID)
	if err != nil {
		t.Fatalf("load plan from fresh scheduler: %v", err)
	}
	if loaded.ID != plan.ID || loaded.Kind != plan.Kind || loaded.Metadata["message_text"] != "hello from store" {
		t.Fatalf("expected fresh scheduler to inspect stored plan, got %+v", loaded)
	}
	plans := fresh.Plans()
	if len(plans) != 1 || plans[0].ID != plan.ID {
		t.Fatalf("expected fresh scheduler to list persisted plans, got %+v", plans)
	}

	if err := fresh.Cancel(plan.ID); err != nil {
		t.Fatalf("cancel persisted schedule: %v", err)
	}
	if _, err := store.LoadSchedulePlan(context.Background(), plan.ID); err == nil {
		t.Fatal("expected cancelled persisted schedule to be deleted")
	}
	if got := fresh.Plans(); len(got) != 0 {
		t.Fatalf("expected no plans after cancel, got %+v", got)
	}
}

func TestSchedulerStoreRemovesFiredDelayPlanAfterAutoRun(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	scheduler := NewScheduler()
	start := time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC)
	current := start
	scheduler.now = func() time.Time { return current }
	scheduler.SetStore(store)
	if err := scheduler.Register(SchedulePlan{ID: "delay-store-auto", Kind: ScheduleKindDelay, Delay: 20 * time.Millisecond, Source: "scheduler", EventType: "schedule.triggered"}); err != nil {
		t.Fatalf("register delay schedule: %v", err)
	}

	events := make(chan eventmodel.Event, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := scheduler.Start(ctx, 5*time.Millisecond, func(event eventmodel.Event) error {
		events <- event
		return nil
	}); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}

	current = start.Add(25 * time.Millisecond)
	select {
	case <-events:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected persisted delay schedule to auto-fire")
	}

	fresh := NewScheduler()
	fresh.SetStore(store)
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		if _, err := fresh.Plan("delay-store-auto"); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expected fired delay schedule to be deleted from store")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSchedulerStoreUpdatesCronDueAtAfterAutoRun(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	scheduler := NewScheduler()
	current := time.Date(2026, 4, 8, 11, 0, 0, 0, time.UTC)
	scheduler.now = func() time.Time { return current }
	scheduler.SetStore(store)
	if err := scheduler.Register(SchedulePlan{ID: "cron-store-auto", Kind: ScheduleKindCron, CronExpr: "* * * * *", Source: "scheduler", EventType: "schedule.triggered"}); err != nil {
		t.Fatalf("register cron schedule: %v", err)
	}
	before, err := store.LoadSchedulePlan(context.Background(), "cron-store-auto")
	if err != nil {
		t.Fatalf("load persisted cron schedule before run: %v", err)
	}

	events := make(chan eventmodel.Event, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := scheduler.Start(ctx, 5*time.Millisecond, func(event eventmodel.Event) error {
		events <- event
		return nil
	}); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}

	current = current.Add(time.Minute)
	select {
	case <-events:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected persisted cron schedule to auto-fire")
	}

	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		after, err := store.LoadSchedulePlan(context.Background(), "cron-store-auto")
		if err != nil {
			t.Fatalf("load persisted cron schedule after run: %v", err)
		}
		if before.DueAt != nil && after.DueAt != nil && after.DueAt.After(*before.DueAt) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected persisted cron dueAt to move forward, before=%+v after=%+v", before, after)
		}
		time.Sleep(10 * time.Millisecond)
	}
	fresh := NewScheduler()
	fresh.SetStore(store)
	if _, err := fresh.Plan("cron-store-auto"); err != nil {
		t.Fatalf("expected cron schedule to remain inspectable after auto-run: %v", err)
	}
}

func TestSchedulerStoreRetainsDelayPlanAfterDispatchFailure(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	scheduler := NewScheduler()
	start := time.Date(2026, 4, 8, 11, 30, 0, 0, time.UTC)
	current := start
	scheduler.now = func() time.Time { return current }
	scheduler.SetStore(store)
	if err := scheduler.Register(SchedulePlan{ID: "delay-store-fail", Kind: ScheduleKindDelay, Delay: 20 * time.Millisecond, Source: "scheduler", EventType: "schedule.triggered"}); err != nil {
		t.Fatalf("register delay schedule: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	if err := scheduler.Start(ctx, 5*time.Millisecond, func(event eventmodel.Event) error {
		attempts++
		return context.Canceled
	}); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}

	current = start.Add(25 * time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	if attempts == 0 {
		t.Fatal("expected failed delay schedule dispatch to be attempted")
	}
	fresh := NewScheduler()
	fresh.SetStore(store)
	if _, err := fresh.Plan("delay-store-fail"); err != nil {
		t.Fatalf("expected failed delay schedule to remain persisted for retry, got %v", err)
	}
}

func TestSchedulerRestoreUsesCreatedAtForDelayPlanWhenDueAtMissing(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	createdAt := time.Date(2026, 4, 8, 15, 0, 0, 0, time.UTC)
	plan := SchedulePlan{ID: "delay-missing-dueat", Kind: ScheduleKindDelay, Delay: 30 * time.Second, Source: "scheduler", EventType: "schedule.triggered"}
	if err := store.SaveSchedulePlan(context.Background(), storedSchedulePlan{Plan: plan, DueAt: nil, DueAtEvidence: "", CreatedAt: createdAt, UpdatedAt: createdAt}); err != nil {
		t.Fatalf("save schedule plan: %v", err)
	}

	scheduler := NewScheduler()
	scheduler.now = func() time.Time { return createdAt.Add(10 * time.Minute) }
	scheduler.SetStore(store)
	if err := scheduler.Restore(context.Background()); err != nil {
		t.Fatalf("restore scheduler: %v", err)
	}

	scheduler.mu.RLock()
	restoredDueAt, ok := scheduler.dueAt[plan.ID]
	scheduler.mu.RUnlock()
	if !ok {
		t.Fatalf("expected restored dueAt for %q", plan.ID)
	}
	want := createdAt.Add(plan.Delay)
	if !restoredDueAt.Equal(want) {
		t.Fatalf("expected dueAt=%s, got %s", want, restoredDueAt)
	}
}

func TestRestoredScheduleDueAt(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 9, 16, 0, 0, 0, time.UTC)
	createdAt := time.Date(2026, 4, 9, 15, 0, 0, 0, time.UTC)
	oneShotAt := time.Date(2026, 4, 9, 18, 0, 0, 0, time.UTC)
	existingDueAt := time.Date(2026, 4, 9, 17, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		stored  storedSchedulePlan
		want    *time.Time
		wantErr bool
	}{
		{
			name:   "keeps explicit dueAt",
			stored: storedSchedulePlan{Plan: SchedulePlan{ID: "schedule-explicit", Kind: ScheduleKindDelay, Delay: 30 * time.Second, Source: "scheduler", EventType: "schedule.triggered"}, DueAt: &existingDueAt, CreatedAt: createdAt},
			want:   &existingDueAt,
		},
		{
			name:   "delay uses createdAt plus delay",
			stored: storedSchedulePlan{Plan: SchedulePlan{ID: "schedule-delay", Kind: ScheduleKindDelay, Delay: 30 * time.Second, Source: "scheduler", EventType: "schedule.triggered"}, CreatedAt: createdAt},
			want:   ptrTime(createdAt.Add(30 * time.Second)),
		},
		{
			name:   "oneshot uses executeAt",
			stored: storedSchedulePlan{Plan: SchedulePlan{ID: "schedule-oneshot", Kind: ScheduleKindOneShot, ExecuteAt: oneShotAt, Source: "scheduler", EventType: "schedule.triggered"}, CreatedAt: createdAt},
			want:   &oneShotAt,
		},
		{
			name:   "cron computes next due from now",
			stored: storedSchedulePlan{Plan: SchedulePlan{ID: "schedule-cron", Kind: ScheduleKindCron, CronExpr: "* * * * *", Source: "scheduler", EventType: "schedule.triggered"}, CreatedAt: createdAt},
			want:   ptrTime(now.Add(1 * time.Minute)),
		},
		{
			name:   "delay without createdAt returns nil",
			stored: storedSchedulePlan{Plan: SchedulePlan{ID: "schedule-delay-nil", Kind: ScheduleKindDelay, Delay: 30 * time.Second, Source: "scheduler", EventType: "schedule.triggered"}},
			want:   nil,
		},
		{
			name:   "oneshot without executeAt returns nil",
			stored: storedSchedulePlan{Plan: SchedulePlan{ID: "schedule-oneshot-nil", Kind: ScheduleKindOneShot, Source: "scheduler", EventType: "schedule.triggered"}, CreatedAt: createdAt},
			want:   nil,
		},
		{
			name:    "invalid cron returns error",
			stored:  storedSchedulePlan{Plan: SchedulePlan{ID: "schedule-cron-invalid", Kind: ScheduleKindCron, CronExpr: "not-a-cron", Source: "scheduler", EventType: "schedule.triggered"}, CreatedAt: createdAt},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := restoredScheduleDueAt(func() time.Time { return now }, tc.stored)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.want == nil {
				if got != nil {
					t.Fatalf("expected nil dueAt, got %v", *got)
				}
				return
			}
			if got == nil || !got.Equal(*tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}

func TestSchedulerStoreRetainsCronDueAtAfterDispatchFailure(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	scheduler := NewScheduler()
	current := time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)
	scheduler.now = func() time.Time { return current }
	scheduler.SetStore(store)
	if err := scheduler.Register(SchedulePlan{ID: "cron-store-fail", Kind: ScheduleKindCron, CronExpr: "* * * * *", Source: "scheduler", EventType: "schedule.triggered"}); err != nil {
		t.Fatalf("register cron schedule: %v", err)
	}
	before, err := store.LoadSchedulePlan(context.Background(), "cron-store-fail")
	if err != nil {
		t.Fatalf("load persisted cron schedule before failure: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := scheduler.Start(ctx, 5*time.Millisecond, func(event eventmodel.Event) error {
		return context.Canceled
	}); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}

	current = current.Add(time.Minute)
	time.Sleep(50 * time.Millisecond)
	after, err := store.LoadSchedulePlan(context.Background(), "cron-store-fail")
	if err != nil {
		t.Fatalf("load persisted cron schedule after failure: %v", err)
	}
	if before.DueAt == nil || after.DueAt == nil || !after.DueAt.Equal(*before.DueAt) {
		t.Fatalf("expected failed cron dispatch to keep persisted dueAt unchanged, before=%+v after=%+v", before, after)
	}
}

func TestSchedulerRestoreLoadsPersistedPlansIntoRuntimeState(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	createdAt := time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)
	dueAt := time.Date(2026, 4, 8, 12, 0, 30, 0, time.UTC)
	if err := store.SaveSchedulePlan(context.Background(), storedSchedulePlan{
		Plan: SchedulePlan{
			ID:        "restore-delay",
			Kind:      ScheduleKindDelay,
			Delay:     30 * time.Second,
			Source:    "scheduler",
			EventType: "schedule.triggered",
		},
		DueAt:     &dueAt,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}); err != nil {
		t.Fatalf("save delay schedule plan: %v", err)
	}

	scheduler := NewScheduler()
	scheduler.now = func() time.Time { return createdAt.Add(5 * time.Second) }
	scheduler.SetStore(store)
	if err := scheduler.Restore(context.Background()); err != nil {
		t.Fatalf("restore scheduler: %v", err)
	}

	plan, err := scheduler.Plan("restore-delay")
	if err != nil {
		t.Fatalf("inspect restored plan: %v", err)
	}
	if plan.ID != "restore-delay" || plan.Kind != ScheduleKindDelay {
		t.Fatalf("expected restored delay plan, got %+v", plan)
	}
	if got := scheduler.dueAt["restore-delay"]; !got.Equal(dueAt) {
		t.Fatalf("expected restored dueAt %s, got %s", dueAt, got)
	}
	snapshot := scheduler.LastRecoverySnapshot()
	if snapshot.TotalSchedules != 1 || snapshot.RecoveredSchedules != 0 || snapshot.InvalidSchedules != 0 {
		t.Fatalf("expected recovery snapshot for restored persisted delay plan, got %+v", snapshot)
	}
	if snapshot.ScheduleKinds[ScheduleKindDelay] != 1 {
		t.Fatalf("expected delay kind count in recovery snapshot, got %+v", snapshot.ScheduleKinds)
	}
}

func TestSchedulerRestoreCountsRecoveredMissingDueAtAndInvalidPlans(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	createdAt := time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC)
	if err := store.SaveSchedulePlan(context.Background(), storedSchedulePlan{
		Plan: SchedulePlan{
			ID:        "restore-delay-missing-dueat",
			Kind:      ScheduleKindDelay,
			Delay:     30 * time.Second,
			Source:    "scheduler",
			EventType: "schedule.triggered",
		},
		DueAt:     nil,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}); err != nil {
		t.Fatalf("save delay schedule plan without dueAt: %v", err)
	}
	if err := store.SaveSchedulePlan(context.Background(), storedSchedulePlan{
		Plan: SchedulePlan{
			ID:        "restore-cron-invalid",
			Kind:      ScheduleKindCron,
			CronExpr:  "bad cron",
			Source:    "scheduler",
			EventType: "schedule.triggered",
		},
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}); err == nil {
		t.Fatal("expected direct save of invalid persisted schedule to fail validation")
	}
	_, err := store.db.ExecContext(context.Background(), `
	INSERT INTO schedule_plans (
	  schedule_id, kind, cron_expr, delay_ms, execute_at, due_at, due_at_evidence, source, event_type,
	  metadata_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "restore-cron-invalid", string(ScheduleKindCron), "bad cron", 0, nil, nil, "", "scheduler", "schedule.triggered", "{}", createdAt.Format(time.RFC3339Nano), createdAt.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("insert invalid persisted schedule plan: %v", err)
	}

	metrics := NewMetricsRegistry()
	scheduler := NewScheduler()
	scheduler.now = func() time.Time { return createdAt.Add(10 * time.Minute) }
	scheduler.SetStore(store)
	scheduler.SetObservability(nil, nil, metrics)
	if err := scheduler.Restore(context.Background()); err != nil {
		t.Fatalf("restore scheduler: %v", err)
	}

	snapshot := scheduler.LastRecoverySnapshot()
	if snapshot.TotalSchedules != 2 || snapshot.RecoveredSchedules != 1 || snapshot.InvalidSchedules != 1 {
		t.Fatalf("expected recovery snapshot counts for missing dueAt and invalid persisted schedule, got %+v", snapshot)
	}
	if snapshot.ScheduleKinds[ScheduleKindDelay] != 1 {
		t.Fatalf("expected recovered delay kind count, got %+v", snapshot.ScheduleKinds)
	}
	if got := scheduler.dueAt["restore-delay-missing-dueat"]; !got.Equal(createdAt.Add(30 * time.Second)) {
		t.Fatalf("expected missing dueAt to be recomputed, got %s", got)
	}
	repaired, err := store.LoadSchedulePlan(context.Background(), "restore-delay-missing-dueat")
	if err != nil {
		t.Fatalf("load repaired persisted schedule plan: %v", err)
	}
	if repaired.DueAt == nil || !repaired.DueAt.Equal(createdAt.Add(30*time.Second)) {
		t.Fatalf("expected repaired dueAt to persist back to storage, got %+v", repaired)
	}
	if repaired.DueAtEvidence != scheduleDueAtEvidenceRecoveredStartup {
		t.Fatalf("expected repaired dueAt evidence to mark startup recovery, got %+v", repaired)
	}
	if _, ok := scheduler.dueAt["restore-cron-invalid"]; ok {
		t.Fatal("expected invalid persisted schedule to be skipped during restore")
	}
	if output := metrics.RenderPrometheus(); !strings.Contains(output, "bot_platform_schedule_recoveries_total 1") {
		t.Fatalf("expected metrics to count recovered schedules, got %s", output)
	}
}

func TestSchedulerStartRestoresPersistedDelayPlanAndFires(t *testing.T) {
	t.Parallel()

	store := openTempSQLiteStore(t)
	defer func() { _ = store.Close() }()

	start := time.Date(2026, 4, 8, 13, 0, 0, 0, time.UTC)
	dueAt := start.Add(20 * time.Millisecond)
	if err := store.SaveSchedulePlan(context.Background(), storedSchedulePlan{
		Plan: SchedulePlan{
			ID:        "restore-delay-fire",
			Kind:      ScheduleKindDelay,
			Delay:     20 * time.Millisecond,
			Source:    "scheduler",
			EventType: "schedule.triggered",
		},
		DueAt:     &dueAt,
		CreatedAt: start,
		UpdatedAt: start,
	}); err != nil {
		t.Fatalf("save delay schedule plan: %v", err)
	}

	current := start
	scheduler := NewScheduler()
	scheduler.now = func() time.Time { return current }
	scheduler.SetStore(store)

	events := make(chan eventmodel.Event, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := scheduler.Start(ctx, 5*time.Millisecond, func(event eventmodel.Event) error {
		events <- event
		return nil
	}); err != nil {
		t.Fatalf("start restored scheduler: %v", err)
	}

	current = start.Add(25 * time.Millisecond)
	select {
	case event := <-events:
		if event.System == nil || event.System.Name != "restore-delay-fire" {
			t.Fatalf("unexpected restored event %+v", event)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected restored delay plan to auto-fire")
	}

	fresh := NewScheduler()
	fresh.SetStore(store)
	if _, err := fresh.Plan("restore-delay-fire"); err == nil {
		t.Fatal("expected restored delay plan to be removed from store after firing")
	}
}
